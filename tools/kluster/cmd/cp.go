package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/hostkey"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kubeconfig"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/local"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/provision"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/remote"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/routeproof"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/sshgate"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stages"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/vpn"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/wgconf"
)

func init() {
	cpCmd.AddCommand(cpInitCmd)
	rootCmd.AddCommand(cpCmd)
}

var cpCmd = &cobra.Command{
	Use:   "cp",
	Short: "The Control Plane",
}

var cpInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Build the Control Plane into an empty slot (docs/runbook/control-plane.md)",
	Long: "Creates k8s-cp only when the project has none, or resumes a build kluster started. " +
		"Opens bootstrap SSH on the Control Plane firewall, creates the server with a seeded host key, " +
		"runs each runbook step as a Stage, proves the operator route through the new hub with " +
		"kluster's own WireGuard peer, edits the laptop's WireGuard config and kubeconfig, and " +
		"closes SSH with an API readback, failed runs included. Run `kluster shared` first.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, cpInit)
	},
}

func cpInit(ctx context.Context, a *app) (err error) {
	cp := a.cfg.ControlPlane
	srv, err := a.api.Server(ctx, cp.Name)
	if err != nil {
		return err
	}
	if srv != nil {
		// kluster pins only servers it created, so a k8s-cp it has no pin
		// for is the live Control Plane or a hand-built one.
		ours, err := a.pins.Has(srv.PublicIP)
		if err != nil {
			return err
		}
		if !ours {
			return fmt.Errorf("%s exists at %s and kluster did not create it: the slot is not empty, so cp init refuses (ADR 0009); "+
				"a deliberate replacement is the manual procedure in infra/README.md", cp.Name, srv.PublicIP)
		}
		fmt.Fprintf(a.out, "%s exists at %s and kluster created it: resuming its build\n", cp.Name, srv.PublicIP)
	}

	g, err := a.gate(ctx)
	if err != nil {
		return err
	}
	// The close runs whatever happens next (ADR 0009), an interrupt
	// included, so it gets a context the interrupt does not cancel.
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
		defer cancel()
		if cerr := g.Close(closeCtx); cerr != nil {
			err = errors.Join(err, cerr)
		}
	}()
	if err := g.Open(ctx, sshgate.ControlPlane); err != nil {
		return err
	}

	if srv == nil {
		if srv, err = a.createControlPlane(ctx); err != nil || srv == nil {
			return err
		}
	} else if !a.mode.Apply {
		fmt.Fprintln(a.out, "Plan Mode: --apply opens SSH, logs in and runs every Stage whose Probe says it is not done")
		return nil
	}
	return a.buildControlPlane(ctx, srv)
}

// createControlPlane creates k8s-cp with a seeded host key and the build
// marker, checks it over the API, and pins the key. In Plan Mode it prints
// what the build would run and returns nil.
func (a *app) createControlPlane(ctx context.Context) (*cloud.Server, error) {
	cp := a.cfg.ControlPlane
	m, err := a.module(ctx, config.ModuleControlPlane)
	if err != nil {
		return nil, err
	}
	kp, err := provision.CreateControlPlane(ctx, m, []hostkey.File{stages.MarkerFile()}, a.mode, a.out)
	if err != nil {
		return nil, err
	}
	if kp == nil {
		return nil, a.planNewControlPlane(ctx)
	}
	srv, err := a.api.Server(ctx, cp.Name)
	if err != nil {
		return nil, err
	}
	if srv == nil {
		return nil, fmt.Errorf("terraform created %s, but the API has no such server", cp.Name)
	}
	if err := checkNewServer(srv, cp.PrivateIP); err != nil {
		return nil, err
	}
	fmt.Fprintf(a.out, "read back: %s id %s, %s, public %s, private %v, delete and rebuild protected\n",
		srv.Name, srv.ID, srv.Status, srv.PublicIP, srv.PrivateIPs)
	if err := a.pins.Set(srv.PublicIP, kp.Public); err != nil {
		return nil, err
	}
	fmt.Fprintf(a.out, "pinned the seeded host key of %s at %s\n", cp.Name, srv.PublicIP)
	return srv, nil
}

// checkNewServer is the runbook's API readback of a new Control Plane.
func checkNewServer(s *cloud.Server, privateIP string) error {
	var errs []error
	if !s.DeleteProtected || !s.RebuildProtected {
		errs = append(errs, errors.New("delete or rebuild protection is off (ADR 0002)"))
	}
	if len(s.PrivateIPs) != 1 || s.PrivateIPs[0] != privateIP {
		errs = append(errs, fmt.Errorf("private addresses %v, want [%s]", s.PrivateIPs, privateIP))
	}
	if s.PublicIP == "" {
		errs = append(errs, errors.New("no public IPv4"))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("%s read back over the API: %w", s.Name, err)
	}
	return nil
}

// planNewControlPlane prints the build of a server that does not exist yet.
func (a *app) planNewControlPlane(ctx context.Context) error {
	srv := &cloud.Server{ID: "<new>", PublicIP: "<public IP>"}
	hostStages, err := a.cpStages(srv)
	if err != nil {
		return err
	}
	all := append([]stage.Stage{stages.RotateHostKey{}}, hostStages...)
	if err := stage.PlanNew(ctx, all, a.cfg.ControlPlane.Name, a.out); err != nil {
		return err
	}
	l := a.laptop()
	fmt.Fprintf(a.out, "then: prove the operator route with kluster's own peer — %s\n", routeproof.Runbook)
	fmt.Fprintf(a.out, "then: set the hub peer in %s; write the kubeconfig to %s (merge: %v)\n", l.WireGuardConf, l.Kubeconfig, l.MergeKubeconfig)
	return stage.PlanNew(ctx, []stage.Stage{stages.CompleteBuild{}}, a.cfg.ControlPlane.Name, a.out)
}

// buildControlPlane runs every Stage on the Control Plane and the laptop.
// Each Stage whose Probe says done is skipped, so a stopped build resumes.
func (a *app) buildControlPlane(ctx context.Context, srv *cloud.Server) error {
	cp := a.cfg.ControlPlane
	opts := stage.Options{Apply: true, Out: a.out}

	auth, err := remote.Auth(a.cfg.SSH.IdentityFile)
	if err != nil {
		return err
	}
	// The callback is made at each dial: knownhosts reads the pins file
	// once, and the rotation changes it.
	dialRoot := func(wait time.Duration) (*remote.Client, error) {
		cb, err := a.pins.Callback()
		if err != nil {
			return nil, err
		}
		wctx, cancel := context.WithTimeout(ctx, wait)
		defer cancel()
		return remote.DialWait(wctx, srv.PublicIP, remote.Config{User: "root", Auth: auth, HostKeyCallback: cb}, 5*time.Second)
	}
	c, err := dialRoot(5 * time.Minute)
	if err != nil {
		return fmt.Errorf("first login to %s: %w", cp.Name, err)
	}
	h := &stage.Host{Name: cp.Name, Addr: srv.PublicIP, Exec: c}
	defer func() {
		if c, ok := h.Exec.(*remote.Client); ok && c != nil {
			_ = c.Close()
		}
	}()
	fmt.Fprintf(a.out, "[%s] login as root verified against the pinned host key\n", cp.Name)

	switch m, err := stages.ReadMarker(ctx, h); {
	case err != nil:
		return err
	case m == stages.MarkerComplete:
		fmt.Fprintf(a.out, "[%s] %s = %s: the build is complete, nothing to do\n", cp.Name, stages.MarkerPath, m)
		return nil
	case m != stages.MarkerRunning:
		return fmt.Errorf("%s has no kluster build marker (%s = %q): it is not a build kluster started, refusing", cp.Name, stages.MarkerPath, m)
	}

	// The rotation restarts sshd, so the Stages after it get a new session
	// that verifies the new key.
	if err := stage.Run(ctx, []stage.Stage{stages.RotateHostKey{Pins: a.pins, Dial: a.dialer()}}, h, opts); err != nil {
		return err
	}
	_ = c.Close()
	h.Exec = nil
	if c, err = dialRoot(time.Minute); err != nil {
		return fmt.Errorf("login after the host key rotation: %w", err)
	}
	h.Exec = c

	hostStages, err := a.cpStages(srv)
	if err != nil {
		return err
	}
	if err := stage.Run(ctx, hostStages, h, opts); err != nil {
		return err
	}

	// The same host answers on its VPN address (control-plane.md, appendix).
	if err := a.pinVPNAddress(ctx, h); err != nil {
		return err
	}
	hubKey, err := stages.HubPublicKey(ctx, h)
	if err != nil {
		return err
	}
	adminConf, err := c.Run(ctx, "cat /etc/kubernetes/admin.conf")
	if err != nil {
		return err
	}
	l := a.laptop()
	names := kubeconfig.NamesFor(l.ClusterName)
	laptopConf, err := kubeconfig.ForLaptop([]byte(adminConf), names, "https://"+cp.VpnIP+":6443")
	if err != nil {
		return err
	}
	if err := a.proveRoute(ctx, h, srv, hubKey, auth, laptopConf); err != nil {
		return err
	}
	if err := a.runLaptopStages(ctx, srv, hubKey, names, laptopConf, opts); err != nil {
		return err
	}
	return stage.Run(ctx, []stage.Stage{stages.CompleteBuild{}}, h, opts)
}

// cpStages are the Control Plane's runbook steps 2 to 8, in order.
func (a *app) cpStages(srv *cloud.Server) ([]stage.Stage, error) {
	c := a.cfg
	cp := c.ControlPlane
	subnet, err := netip.ParsePrefix(c.WireGuard.Subnet)
	if err != nil {
		return nil, fmt.Errorf("wireguard.subnet: %w", err)
	}
	read := func(p string) (string, error) {
		path, err := c.Path(p)
		if err != nil {
			return "", err
		}
		b, err := os.ReadFile(path)
		return string(b), err
	}
	flannel, err := read(c.Manifests.Flannel)
	if err != nil {
		return nil, err
	}
	ccm, err := read(c.Manifests.CCM)
	if err != nil {
		return nil, err
	}
	csi, err := read(c.Manifests.CSI)
	if err != nil {
		return nil, err
	}
	return []stage.Stage{
		stages.PrivateNetwork{Interface: c.Node.NetworkInterface, MAC: srv.PrivateMAC, IP: cp.PrivateIP},
		stages.BaseHost{User: cp.SSHUser, Containerd: c.Versions.Containerd, Runc: c.Versions.Runc, CNIPlugins: c.Versions.CNIPlugins},
		stages.WireGuardHub{Address: cp.VpnIP + "/" + strconv.Itoa(subnet.Bits()), Port: c.WireGuard.Port, Peers: c.WireGuard.Peers},
		stages.KubePrep{Minor: c.Versions.Kubernetes, Endpoint: cp.Endpoint, PrivateIP: cp.PrivateIP},
		stages.KubeadmInit{Endpoint: cp.Endpoint, PrivateIP: cp.PrivateIP, VpnIP: cp.VpnIP, PublicIP: srv.PublicIP,
			PodCIDR: c.Cluster.PodCIDR, ServiceCIDR: c.Cluster.ServiceCIDR},
		stages.Flannel{Manifest: flannel, Node: cp.Name, PrivateIP: cp.PrivateIP, Interface: c.Node.NetworkInterface, PodMTU: c.Cluster.PodMTU},
		stages.CloudController{Token: a.env.HcloudToken(), Network: c.Cluster.HcloudNetwork, Manifest: ccm,
			Node: cp.Name, ServerID: srv.ID, PrivateIP: cp.PrivateIP},
		stages.CSI{Manifest: csi},
	}, nil
}

// pinVPNAddress pins the host key, read over the verified session, at the
// Control Plane's VPN address.
func (a *app) pinVPNAddress(ctx context.Context, h *stage.Host) error {
	out, err := h.Exec.Run(ctx, "cat /etc/ssh/ssh_host_ed25519_key.pub")
	if err != nil {
		return err
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(out)))
	if err != nil {
		return fmt.Errorf("reading the host key: %w", err)
	}
	return a.pins.Set(a.cfg.ControlPlane.VpnIP, key)
}

func (a *app) proveRoute(ctx context.Context, h *stage.Host, srv *cloud.Server, hubKey string, auth ssh.AuthMethod, laptopConf []byte) error {
	cp := a.cfg.ControlPlane
	fmt.Fprintf(a.out, "[%s] prove the operator route through the hub — %s\n", cp.Name, routeproof.Runbook)
	key, err := vpn.ParseKey(hubKey)
	if err != nil {
		return err
	}
	pub, err := netip.ParseAddr(srv.PublicIP)
	if err != nil {
		return err
	}
	cb, err := a.pins.Callback()
	if err != nil {
		return err
	}
	return routeproof.Proof{
		Hub:        h,
		HubKey:     key,
		Endpoint:   netip.AddrPortFrom(pub, uint16(a.cfg.WireGuard.Port)),
		HubIP:      netip.MustParseAddr(cp.VpnIP),
		ProbeIP:    netip.MustParseAddr(a.cfg.WireGuard.ProbeIP),
		User:       cp.SSHUser,
		Hostname:   cp.Name,
		Auth:       auth,
		HostKeys:   cb,
		Kubeconfig: laptopConf,
		Out:        a.out,
	}.Run(ctx)
}

// runLaptopStages edits the operator's WireGuard config and kubeconfig, or
// in a rehearsal their stand-ins.
func (a *app) runLaptopStages(ctx context.Context, srv *cloud.Server, hubKey string, names kubeconfig.Names, conf []byte, opts stage.Options) error {
	cp := a.cfg.ControlPlane
	l := a.laptop()
	wgPath, err := a.cfg.Path(l.WireGuardConf)
	if err != nil {
		return err
	}
	kcPath, err := a.cfg.Path(l.Kubeconfig)
	if err != nil {
		return err
	}
	var seed string
	if !a.env.IsLive() {
		seed = wgconf.RehearsalCopy(laptopAddress(a.cfg.WireGuard.Peers), a.cfg.WireGuard.Subnet)
	}
	wg := stages.LaptopWireGuard{
		Path:  wgPath,
		Unit:  l.WireGuardUnit,
		Sudo:  l.Sudo,
		HubIP: netip.MustParseAddr(cp.VpnIP),
		Hub:   wgconf.HubPeer{PublicKey: hubKey, Endpoint: fmt.Sprintf("%s:%d", srv.PublicIP, a.cfg.WireGuard.Port)},
		Seed:  seed,
	}
	if err := stage.Run(ctx, []stage.Stage{wg}, &stage.Host{Name: "laptop", Exec: local.Exec{Sudo: l.Sudo, TmpDir: a.run.Path()}}, opts); err != nil {
		return err
	}
	kc := stages.LaptopKubeconfig{Path: kcPath, Merge: l.MergeKubeconfig, Names: names, Conf: conf, TmpDir: a.run.Path()}
	return stage.Run(ctx, []stage.Stage{kc}, &stage.Host{Name: "laptop", Exec: local.Exec{TmpDir: a.run.Path()}}, opts)
}

// laptopAddress is the operator laptop's VPN address: the first hub peer in
// kluster.yaml is the operator's own.
func laptopAddress(peers []config.Peer) string {
	if len(peers) == 0 {
		return ""
	}
	return peers[0].AllowedIPs
}

func (a *app) laptop() config.Laptop { return a.cfg.Envs[a.env.Name].Laptop }
