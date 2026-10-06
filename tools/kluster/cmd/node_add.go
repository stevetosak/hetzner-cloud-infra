package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/filediff"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/provision"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/remote"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/sshgate"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stages"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/wgconf"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

func init() {
	nodeCmd.AddCommand(nodeAddCmd)
}

var nodeAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add one Worker: declare it in the Worker set, then create, build and join it (docs/runbook/workers.md)",
	Long: "Run twice. A name the environment's Worker set does not declare gets its entry first: the " +
		"lowest free private and VPN address, written to the set under --apply. kluster then stops, " +
		"because it runs no Terraform on a Worker set git does not hold: commit the edit and run again. " +
		"The second run creates exactly that server with a seeded host key, rotates the key, runs the " +
		"Workers runbook as Stages, adds the Worker to the hub, joins it with a 15-minute token and waits " +
		"for its Node. Bootstrap SSH is closed at the end with an API readback, failed runs included. " +
		"A run that stopped resumes a server kluster created.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return run(cmd, func(ctx context.Context, a *app) error { return nodeAdd(ctx, a, args[0]) })
	},
}

// nodeReadyTimeout is how long node add waits for the cloud controller to
// initialize the new Node.
const nodeReadyTimeout = 5 * time.Minute

// dnsLabel is a valid server and Node name.
var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func checkWorkerName(name, controlPlane string) error {
	if !dnsLabel.MatchString(name) {
		return fmt.Errorf("%q is not a DNS label (lower-case letters, digits and -, at most 63)", name)
	}
	if name == controlPlane {
		return fmt.Errorf("%s is the Control Plane", name)
	}
	return nil
}

func nodeAdd(ctx context.Context, a *app, name string) (err error) {
	if err := checkWorkerName(name, a.cfg.ControlPlane.Name); err != nil {
		return err
	}
	path, err := a.workerSetPath()
	if err != nil {
		return err
	}
	set, err := workerset.Load(path)
	if err != nil {
		return err
	}
	srv, err := a.api.Server(ctx, name)
	if err != nil {
		return err
	}
	w, declared := set.Get(name)
	if !declared {
		if srv != nil {
			return fmt.Errorf("a server %s exists at %s but %s does not declare it: Terraform would refuse the duplicate name, so node add refuses", name, srv.PublicIP, path)
		}
		return a.declareWorker(ctx, set, name)
	}

	// The commit gate runs before anything opens.
	workers, err := a.module(ctx, config.ModuleWorkers)
	if err != nil {
		return err
	}
	cpSrv, err := a.api.Server(ctx, a.cfg.ControlPlane.Name)
	if err != nil {
		return err
	}
	if cpSrv == nil {
		return fmt.Errorf("the project has no %s: run kluster cp init first", a.cfg.ControlPlane.Name)
	}
	if srv != nil {
		// kluster pins only servers it created.
		ours, err := a.pins.Has(srv.PublicIP)
		if err != nil {
			return err
		}
		if !ours {
			return fmt.Errorf("%s exists at %s and kluster did not create it: node add refuses; node replace builds it again", name, srv.PublicIP)
		}
		fmt.Fprintf(a.out, "%s exists at %s and kluster created it: resuming its build\n", name, srv.PublicIP)
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
	// A rehearsal reaches its Control Plane through bootstrap SSH too.
	fws := []sshgate.Firewall{sshgate.Worker}
	if !a.env.IsLive() {
		fws = append(fws, sshgate.ControlPlane)
	}
	if err := g.Open(ctx, fws...); err != nil {
		return err
	}
	if srv != nil && !a.mode.Apply {
		fmt.Fprintln(a.out, "Plan Mode: --apply opens SSH, logs in and runs every Stage whose Probe says it is not done")
		return nil
	}

	cp, closeCP, err := a.reachControlPlane(ctx)
	if err != nil {
		return err
	}
	defer closeCP()

	if srv == nil {
		if err := a.checkNoNode(ctx, cp, name); err != nil {
			return err
		}
		created, err := provision.CreateWorkers(ctx, workers, []string{name}, a.pins, a.mode, a.out)
		if err != nil {
			return err
		}
		if len(created) == 0 {
			return a.planNewWorker(ctx, w, cpSrv, cp)
		}
		if srv, err = a.api.Server(ctx, name); err != nil {
			return err
		}
		if srv == nil {
			return fmt.Errorf("terraform created %s, but the API has no such server", name)
		}
	}
	if err := checkNewWorker(srv, w); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "read back: %s id %s, %s, public %s, private %v\n", srv.Name, srv.ID, srv.Status, srv.PublicIP, srv.PrivateIPs)
	return a.buildWorker(ctx, w, srv, cpSrv, cp)
}

// declareWorker writes a new Worker's entry into the Worker set, on top of
// a set git holds, so the commit the operator makes is exactly that entry.
func (a *app) declareWorker(ctx context.Context, set *workerset.Set, name string) error {
	if err := workerset.CheckCommitted(ctx, set.Path); err != nil {
		return err
	}
	w, err := nextWorker(a.cfg, set, name)
	if err != nil {
		return err
	}
	updated, err := set.With(w)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s declares no worker %s: its entry comes first — private %s, VPN %s, %s\n",
		set.Path, name, w.PrivateIP, w.VpnIP, w.ServerType)
	fmt.Fprint(a.out, filediff.Unified(set.Path, string(set.Bytes()), string(updated)))
	if !a.mode.Apply {
		fmt.Fprintln(a.out, "Plan Mode: --apply writes the entry")
		return nil
	}
	info, err := os.Stat(set.Path)
	if err != nil {
		return err
	}
	if err := os.WriteFile(set.Path, updated, info.Mode().Perm()); err != nil {
		return err
	}
	back, err := workerset.Load(set.Path)
	if err != nil {
		return err
	}
	if got, ok := back.Get(name); !ok || !reflect.DeepEqual(got, w) {
		return fmt.Errorf("read back: %s does not hold the entry written for %s", set.Path, name)
	}
	fmt.Fprintf(a.out, "read back: %s declares %s\n", set.Path, name)
	fmt.Fprintf(a.out, "commit %s, then run kluster node add %s again: kluster runs no Terraform on an uncommitted Worker set\n", set.Path, name)
	return nil
}

// nextWorker is the entry a new Worker gets: the lowest free address of
// each pool, skipping the hub, kluster's probe peer and every operator peer.
func nextWorker(c *config.Config, set *workerset.Set, name string) (workerset.Worker, error) {
	private, err := workerset.ParsePool(c.Workers.PrivateIPs)
	if err != nil {
		return workerset.Worker{}, fmt.Errorf("workers.privateIPs: %w", err)
	}
	vpn, err := workerset.ParsePool(c.Workers.VpnIPs)
	if err != nil {
		return workerset.Worker{}, fmt.Errorf("workers.vpnIPs: %w", err)
	}
	reserved, err := reservedVPN(c)
	if err != nil {
		return workerset.Worker{}, err
	}
	return set.Next(name, c.Workers.ServerType, private, vpn, reserved)
}

func reservedVPN(c *config.Config) ([]netip.Addr, error) {
	out := []netip.Addr{}
	for _, s := range []string{c.ControlPlane.VpnIP, c.WireGuard.ProbeIP} {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	for _, p := range c.WireGuard.Peers {
		pfx, err := netip.ParsePrefix(p.AllowedIPs)
		if err != nil {
			return nil, fmt.Errorf("wireguard peer %s: %w", p.Name, err)
		}
		if !pfx.IsSingleIP() {
			return nil, fmt.Errorf("wireguard peer %s holds %s, not one address", p.Name, p.AllowedIPs)
		}
		out = append(out, pfx.Addr())
	}
	return out, nil
}

// reachControlPlane logs in to the Control Plane when it can be reached:
// always under --apply, which opened the gate; in Plan Mode live through
// WireGuard, and a rehearsal only while bootstrap SSH is already open. A nil
// host means Plan Mode could not reach it.
func (a *app) reachControlPlane(ctx context.Context) (*stage.Host, func(), error) {
	if !a.mode.Apply && !a.env.IsLive() {
		open, err := a.api.SSHOpen(ctx, sshgate.ControlPlane.Name)
		if err != nil {
			return nil, nil, err
		}
		if !open {
			fmt.Fprintf(a.out, "Plan Mode: bootstrap SSH on %s stays closed, so the Control Plane is not read\n", sshgate.ControlPlane.Name)
			return nil, func() {}, nil
		}
	}
	return a.dialControlPlane(ctx)
}

// checkNoNode refuses a new server whose name a Node still holds: a stale
// Node would hand its taints and labels to the new server.
func (a *app) checkNoNode(ctx context.Context, cp *stage.Host, name string) error {
	if cp == nil {
		fmt.Fprintf(a.out, "Plan Mode: not checked that no Node %s exists; --apply checks it before creating\n", name)
		return nil
	}
	nodes, err := kube.Kubectl{Exec: cp.Exec}.Nodes(ctx)
	if err != nil {
		return err
	}
	return noNode(nodes, name)
}

func noNode(nodes []kube.Node, name string) error {
	for _, n := range nodes {
		if n.Name == name {
			return fmt.Errorf("a Node %s exists with no server behind it: node replace deletes it", name)
		}
	}
	return nil
}

// checkNewWorker is the API readback of a Worker's server.
func checkNewWorker(s *cloud.Server, w workerset.Worker) error {
	var errs []error
	if !s.Running() {
		errs = append(errs, fmt.Errorf("status %s, want running", s.Status))
	}
	if len(s.PrivateIPs) != 1 || s.PrivateIPs[0] != w.PrivateIP {
		errs = append(errs, fmt.Errorf("private addresses %v, want [%s]", s.PrivateIPs, w.PrivateIP))
	}
	if s.PrivateMAC == "" {
		errs = append(errs, errors.New("no MAC on the private network"))
	}
	if s.PublicIP == "" {
		errs = append(errs, errors.New("no public IPv4"))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("%s read back over the API: %w", s.Name, err)
	}
	return nil
}

// workerHost is what the Stages of one Worker are built from.
type workerHost struct {
	Worker     workerset.Worker
	Server     *cloud.Server
	CPServer   *cloud.Server
	HubKey     string // the hub's WireGuard public key
	WorkerKey  string // the Worker's WireGuard public key, known once the spoke runs
	subnetBits int
}

// preJoinStages are the Workers runbook's steps 3 to 6 on the Worker.
func (a *app) preJoinStages(h workerHost) []stage.Stage {
	c := a.cfg
	return []stage.Stage{
		stages.At(stages.PrivateNetwork{Interface: c.Node.NetworkInterface, MAC: h.Server.PrivateMAC, IP: h.Worker.PrivateIP},
			"docs/runbook/workers.md#3-base-host-setup"),
		stages.At(stages.BaseHost{User: c.Node.User, Containerd: c.Versions.Containerd, Runc: c.Versions.Runc},
			"docs/runbook/workers.md#3-base-host-setup"),
		stages.At(stages.KubePrep{Minor: c.Versions.Kubernetes, Endpoint: c.ControlPlane.Endpoint,
			EndpointIP: c.ControlPlane.PrivateIP, PrivateIP: h.Worker.PrivateIP, HoldCNI: true},
			"docs/runbook/workers.md#5-kubernetes-packages-and-pre-join-configuration"),
		stages.WireGuardSpoke{
			Address:     h.Worker.VpnIP + "/" + strconv.Itoa(h.subnetBits),
			HubKey:      h.HubKey,
			HubEndpoint: fmt.Sprintf("%s:%d", h.CPServer.PublicIP, c.WireGuard.Port),
			AllowedIPs:  c.WireGuard.Subnet,
		},
	}
}

func (a *app) hubPeer(h workerHost) stage.Stage {
	return stages.HubPeer{
		Peer: wgconf.Peer{Name: h.Worker.Name, PublicKey: h.WorkerKey, AllowedIPs: h.Worker.VpnIP + "/32"},
		Now:  time.Now,
	}
}

func (a *app) nodeReady(h workerHost) stage.Stage {
	return stages.NodeReady{Node: h.Worker.Name, PrivateIP: h.Worker.PrivateIP, ServerID: h.Server.ID,
		Every: 5 * time.Second, Timeout: nodeReadyTimeout}
}

func (a *app) workerHost(w workerset.Worker, srv, cpSrv *cloud.Server) (workerHost, error) {
	subnet, err := netip.ParsePrefix(a.cfg.WireGuard.Subnet)
	if err != nil {
		return workerHost{}, fmt.Errorf("wireguard.subnet: %w", err)
	}
	return workerHost{Worker: w, Server: srv, CPServer: cpSrv, subnetBits: subnet.Bits()}, nil
}

// planNewWorker prints the build of a Worker that does not exist yet.
func (a *app) planNewWorker(ctx context.Context, w workerset.Worker, cpSrv *cloud.Server, cp *stage.Host) error {
	h, err := a.workerHost(w, &cloud.Server{ID: "<new>", PrivateMAC: "<MAC>"}, cpSrv)
	if err != nil {
		return err
	}
	h.HubKey, h.WorkerKey = "<hub public key>", "<worker public key>"
	if cp != nil {
		if h.HubKey, err = stages.WireGuardPublicKey(ctx, cp); err != nil {
			return err
		}
	}
	cpName := a.cfg.ControlPlane.Name
	worker := append([]stage.Stage{stages.RotateHostKey{}}, a.preJoinStages(h)...)
	if err := stage.PlanNew(ctx, worker, w.Name, a.out); err != nil {
		return err
	}
	if err := stage.PlanNew(ctx, []stage.Stage{a.hubPeer(h)}, cpName, a.out); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "then: pin the host key of %s at its VPN address %s\n", w.Name, w.VpnIP)
	join := stages.Join{Endpoint: a.cfg.ControlPlane.Endpoint}
	if err := stage.PlanNew(ctx, []stage.Stage{join}, w.Name, a.out); err != nil {
		return err
	}
	return stage.PlanNew(ctx, []stage.Stage{a.nodeReady(h)}, cpName, a.out)
}

// buildWorker runs every Stage of a new Worker, on it and on the Control
// Plane. Each Stage whose Probe says done is skipped, so a stopped build
// resumes.
func (a *app) buildWorker(ctx context.Context, w workerset.Worker, srv, cpSrv *cloud.Server, cp *stage.Host) error {
	opts := stage.Options{Apply: true, Out: a.out}
	c, err := a.dialRoot(ctx, srv.PublicIP, 5*time.Minute)
	if err != nil {
		return fmt.Errorf("first login to %s: %w", w.Name, err)
	}
	wh := &stage.Host{Name: w.Name, Addr: srv.PublicIP, Exec: c}
	defer func() {
		if c, ok := wh.Exec.(*remote.Client); ok && c != nil {
			_ = c.Close()
		}
	}()
	fmt.Fprintf(a.out, "[%s] login as root verified against the pinned host key\n", w.Name)

	// The rotation restarts sshd, so the Stages after it get a new session
	// that verifies the new key.
	if err := stage.Run(ctx, []stage.Stage{stages.RotateHostKey{Pins: a.pins, Dial: a.dialer()}}, wh, opts); err != nil {
		return err
	}
	_ = c.Close()
	wh.Exec = nil
	if c, err = a.dialRoot(ctx, srv.PublicIP, time.Minute); err != nil {
		return fmt.Errorf("login after the host key rotation: %w", err)
	}
	wh.Exec = c

	h, err := a.workerHost(w, srv, cpSrv)
	if err != nil {
		return err
	}
	if h.HubKey, err = stages.WireGuardPublicKey(ctx, cp); err != nil {
		return err
	}
	if err := stage.Run(ctx, a.preJoinStages(h), wh, opts); err != nil {
		return err
	}
	if h.WorkerKey, err = stages.WireGuardPublicKey(ctx, wh); err != nil {
		return err
	}
	if err := stage.Run(ctx, []stage.Stage{a.hubPeer(h)}, cp, opts); err != nil {
		return err
	}
	// The same host answers on its VPN address once the hub routes to it.
	if err := a.pinVPNAddress(ctx, wh, w.VpnIP); err != nil {
		return err
	}
	// The join runs over the public session: a rehearsal's VPN addresses
	// are the live ones, which the laptop routes to the live hub.
	join := stages.Join{ControlPlane: cp.Exec, Endpoint: a.cfg.ControlPlane.Endpoint}
	if err := stage.Run(ctx, []stage.Stage{join}, wh, opts); err != nil {
		return err
	}
	return stage.Run(ctx, []stage.Stage{a.nodeReady(h)}, cp, opts)
}
