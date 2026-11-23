package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/filediff"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/mesh"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/meshstore"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/nodeset"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/remote"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/sshgate"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stages"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

// byoRelease is the Ubuntu release of the Hetzner image the Workers run
// (infra/workers/variables.tf, a test compares them): the packages kluster
// pins are built for it, so a machine the operator installs runs it too.
const byoRelease = "24.04"

// excludeFromLoadBalancers keeps a byo machine out of every cloud load
// balancer's targets; its kubelet registers it.
const excludeFromLoadBalancers = "node.kubernetes.io/exclude-from-external-load-balancers"

// byoTimeouts are how long a byo build waits for its Node.
const (
	byoRegisterTimeout = 2 * time.Minute
	byoPoll            = 5 * time.Second
)

// fingerprint is a SHA256 host key fingerprint as `ssh-keygen -lf` prints it.
var fingerprint = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

// byoAdd is node add of a machine the operator installed: the first run
// declares it in the Node set, the second builds it.
func byoAdd(ctx context.Context, a *app, name string) error {
	set, err := a.nodeSet()
	if err != nil {
		return err
	}
	m, declared := set.Mesh().Machine(name)
	if !declared {
		return a.declareByo(ctx, set, name, nodeAddFlags.site)
	}
	if s := nodeAddFlags.site; s != "" && s != m.Site.Name {
		return fmt.Errorf("%s declares %s on site %s, not %s: --site only fills a new entry", set.Path, name, m.Site.Name, s)
	}
	return a.buildByo(ctx, set, m)
}

// checkByoReach refuses a byo machine on a site with a public address. Its
// peers would need that address, and neither the Node set nor the mesh store
// holds one: the first such machine, the OVHcloud node of the Providers Plan's
// part C, decides where it lives.
func checkByoReach(name string, site nodeset.Site) error {
	if site.Reach != "nat" {
		return fmt.Errorf("byo machine %s stands on site %s, whose reach is %s: kluster builds a byo machine only behind NAT until the Node set holds a public machine's address (Providers Plan, part C)",
			name, site.Name, site.Reach)
	}
	return nil
}

// declareByo writes a new byo machine's entry into the Node set, on top of a
// file git holds, so the commit the operator makes is exactly that entry.
func (a *app) declareByo(ctx context.Context, set *nodeset.Set, name, siteName string) error {
	if siteName == "" {
		return fmt.Errorf("%s declares no machine %s: --site names the site of a new byo machine", set.Path, name)
	}
	if err := a.checkNotHcloudName(name); err != nil {
		return err
	}
	site, ok := set.Site(siteName)
	if !ok {
		return fmt.Errorf("%s has no site %s", set.Path, siteName)
	}
	if err := checkByoReach(name, site); err != nil {
		return err
	}
	if err := workerset.CheckCommitted(ctx, set.Path, a.out); err != nil {
		return err
	}
	addr, err := set.NextMeshAddress(siteName)
	if err != nil {
		return err
	}
	nm := nodeset.NewMachine{Name: name, Site: siteName, Provisioner: nodeset.ProvisionerByo, MeshAddress: addr}
	updated, err := set.WithMachine(nm)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s declares no machine %s: its entry comes first — site %s, provisioner byo, mesh address %s\n",
		set.Path, name, siteName, addr)
	fmt.Fprint(a.out, filediff.Unified(set.Path, string(set.Bytes()), string(updated)))
	if !a.mode.Apply {
		fmt.Fprintln(a.out, "Plan Mode: --apply writes the entry")
		return nil
	}
	if err := writeBack(set.Path, updated, func() error {
		back, err := nodeset.Load(set.Path)
		if err != nil {
			return err
		}
		got, ok := back.Mesh().Machine(name)
		if !ok || got.Site.Name != siteName || got.Provisioner != nodeset.ProvisionerByo || got.Address != addr {
			return fmt.Errorf("read back: %s does not hold the entry written for %s", set.Path, name)
		}
		return nil
	}); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "read back: %s declares %s\n", set.Path, name)
	fmt.Fprintf(a.out, "commit %s, then run kluster node add %s --bootstrap <its address> --host-key <SHA256:… of /etc/ssh/ssh_host_ed25519_key.pub>, both read on its console\n",
		set.Path, name)
	return nil
}

// checkNotHcloudName refuses a byo machine with the name of the Control
// Plane or of a Worker of the Worker set.
func (a *app) checkNotHcloudName(name string) error {
	path, err := a.workerSetPath()
	if err != nil {
		return err
	}
	workers, err := workerset.Load(path)
	if err != nil {
		return err
	}
	if _, ok := workers.Get(name); ok {
		return fmt.Errorf("%s declares a Hetzner Worker %s: a byo machine needs another name", path, name)
	}
	return nil
}

// buildByo builds a declared byo machine and joins it. Live reaches the
// Control Plane through WireGuard; a rehearsal opens bootstrap SSH on both
// firewalls, because the mesh changes every Hetzner server too.
func (a *app) buildByo(ctx context.Context, set *nodeset.Set, m nodeset.Machine) error {
	if err := checkByoReach(m.Name, m.Site); err != nil {
		return err
	}
	if err := workerset.CheckCommitted(ctx, set.Path, a.out); err != nil {
		return err
	}
	return a.withMeshAccess(ctx, func() error {
		cp, closeCP, err := a.reachControlPlane(ctx)
		if err != nil {
			return err
		}
		defer closeCP()
		if cp == nil {
			fmt.Fprintln(a.out, "Plan Mode: the Control Plane was not reached, so nothing is checked; --apply opens bootstrap SSH and reads it")
			return a.planByo(ctx, m)
		}
		ca, err := a.preflightByo(ctx, set, m, cp)
		if err != nil {
			return err
		}
		if !a.mode.Apply {
			return a.planByo(ctx, m)
		}
		b := &byoBuild{a: a, m: m, cp: cp, ca: ca, bootstrap: nodeAddFlags.bootstrap, hostKey: nodeAddFlags.hostKey}
		return b.run(ctx)
	})
}

// preflightByo refuses a build that cannot work, before anything changes:
// a Node of the cluster that has not joined the mesh, a Hetzner firewall
// that keeps the mesh port closed, or another Node by the machine's name. It
// returns the sha256 of the cluster's CA.
func (a *app) preflightByo(ctx context.Context, set *nodeset.Set, m nodeset.Machine, cp *stage.Host) (string, error) {
	k := kube.Kubectl{Exec: cp.Exec}
	nodes, err := k.Nodes(ctx)
	if err != nil {
		return "", err
	}
	entries, err := meshstore.ConfigMap{Client: k}.Entries(ctx)
	if err != nil {
		return "", err
	}
	errs := []error{checkMeshMembers(nodes, set.Mesh(), entries, m.Name), checkOwnNode(nodes, m)}
	for _, fw := range sshgate.All {
		open, err := a.api.MeshOpen(ctx, fw.Name)
		switch {
		case err != nil:
			errs = append(errs, err)
		case !open:
			errs = append(errs, fmt.Errorf("%s admits no UDP %d: a machine behind NAT reaches no Hetzner server; set allow_mesh_udp in a committed infra/shared/terraform.tfvars (infra/README.md#the-mesh-port)",
				fw.Name, mesh.Port))
		default:
			fmt.Fprintf(a.out, "read back: %s admits UDP %d\n", fw.Name, mesh.Port)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return "", fmt.Errorf("node add %s refused: %w", m.Name, err)
	}
	ca, err := cp.Exec.Run(ctx, "sha256sum < /etc/kubernetes/pki/ca.crt | cut -d' ' -f1")
	if err != nil {
		return "", fmt.Errorf("reading the cluster's CA: %w", err)
	}
	return strings.TrimSpace(ca), nil
}

// checkMeshMembers refuses while a Node of the cluster is not a joined
// machine of the mesh: a byo machine reaches every other Node only over
// wg-mesh, so each needs a peer for it.
func checkMeshMembers(nodes []kube.Node, m *nodeset.Mesh, entries map[string]meshstore.Entry, self string) error {
	var missing []string
	for _, n := range nodes {
		if n.Name == self {
			continue
		}
		_, declared := m.Machine(n.Name)
		if _, joined := entries[n.Name]; !declared || !joined {
			missing = append(missing, n.Name)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("the Nodes %v have not joined the mesh, and a byo machine reaches the cluster only over it: declare them in the Node set's machines and run kluster node mesh --apply first",
			missing)
	}
	return nil
}

// checkOwnNode refuses a Node by the machine's name that another machine
// registered: its providerID would not be the machine's.
func checkOwnNode(nodes []kube.Node, m nodeset.Machine) error {
	for _, n := range nodes {
		if n.Name == m.Name && n.ProviderID != m.ProviderID() {
			return fmt.Errorf("a Node %s exists with providerID %q, not %s: another machine holds the name", m.Name, n.ProviderID, m.ProviderID())
		}
	}
	return nil
}

// byoKubelet is the kubelet of a byo machine: its mesh address, its own
// providerID, no cloud provider to wait for, the best-effort taint at
// registration on such a site, and no cloud load balancer (ADR 0016).
func byoKubelet(m nodeset.Machine) stages.KubeletArgs {
	k := stages.KubeletArgs{NodeIP: m.Address.String(), ProviderID: m.ProviderID(), Labels: []string{excludeFromLoadBalancers + "=true"}}
	if t, ok := m.Site.Taint(); ok {
		k.Taints = []string{t.String()}
	}
	return k
}

// byoBuild is one build of a byo machine under --apply.
type byoBuild struct {
	a         *app
	m         nodeset.Machine
	cp        *stage.Host
	ca        string
	bootstrap string // --bootstrap; empty on a run that resumes over the jump
	hostKey   string // --host-key
}

func (b *byoBuild) opts() stage.Options { return stage.Options{Apply: true, Out: b.a.out} }

// run builds the machine. Every Stage whose Probe says done is skipped, so a
// run that stopped resumes: at the bootstrap address until the machine's key
// is pinned at its mesh address, then through the jump, where every Stage is
// checked again.
func (b *byoBuild) run(ctx context.Context) error {
	a, m := b.a, b.m
	pinned, err := a.pins.Has(m.Address.String())
	if err != nil {
		return err
	}
	if !pinned {
		if err := b.bootstrapBuild(ctx); err != nil {
			return err
		}
	}
	h, closeH, err := a.dialJump(ctx, b.cp, m)
	if err != nil {
		return err
	}
	defer closeH()
	if err := stage.Run(ctx, b.hostStages(), h, b.opts()); err != nil {
		return err
	}
	if err := a.joinMesh(ctx, m.Name, b.cp, h); err != nil {
		return err
	}
	c := a.cfg
	join := []stage.Stage{
		stages.Join{ControlPlane: b.cp.Exec, Endpoint: c.ControlPlane.Endpoint},
		stages.NodeRegistered{ControlPlane: b.cp.Exec, Node: m.Name, Every: byoPoll, Timeout: byoRegisterTimeout},
	}
	if err := stage.Run(ctx, join, h, b.opts()); err != nil {
		return err
	}
	ready := stages.NodeReady{Node: m.Name, InternalIP: m.Address.String(), ProviderID: m.ProviderID(),
		Labels: map[string]string{excludeFromLoadBalancers: "true"}, Every: byoPoll, Timeout: nodeReadyTimeout}
	if err := stage.Run(ctx, []stage.Stage{ready, stages.NodeProfile{Node: m.Name, Site: m.Site}}, b.cp, b.opts()); err != nil {
		return err
	}
	if err := stage.Run(ctx, []stage.Stage{stages.SiteFirewall{Public: m.Site.Reach == "public"}}, h, b.opts()); err != nil {
		return err
	}
	return b.proveClosed(ctx)
}

// hostStages are the machine's own runbook steps: the check, the base host
// and the Kubernetes packages with its kubelet.
func (b *byoBuild) hostStages() []stage.Stage {
	c := b.a.cfg
	return []stage.Stage{
		stages.ByoCheck{Node: b.m.Name, Release: byoRelease, ClusterCA: b.ca},
		stages.BaseHost{User: c.Node.User, KeepUserKeys: true, Containerd: c.Versions.Containerd, Runc: c.Versions.Runc},
		stages.KubePrep{Version: c.Versions.Kubernetes, Endpoint: c.ControlPlane.Endpoint, EndpointIP: c.ControlPlane.PrivateIP,
			Kubelet: byoKubelet(b.m), HoldCNI: true},
	}
}

// bootstrapBuild is the part of the build that runs at the bootstrap
// address: the first login, the host key rotation, the host Stages and the
// mesh. It ends when the jump reaches the machine and its key is pinned at
// its mesh address.
func (b *byoBuild) bootstrapBuild(ctx context.Context) error {
	a, m := b.a, b.m
	h, err := b.firstLogin(ctx)
	if err != nil {
		return err
	}
	defer func() { closeExec(h) }()
	host := b.hostStages()
	rotate := stages.RotateHostKey{Pins: a.pins, Dial: a.dialerAs(a.cfg.Node.User, true)}
	if err := stage.Run(ctx, []stage.Stage{host[0], rotate}, h, b.opts()); err != nil {
		return err
	}
	// The rotation restarts sshd: the Stages after it get a new session
	// that verifies the new key.
	closeExec(h)
	if h.Exec, err = b.dialBootstrap(ctx, nil); err != nil {
		return fmt.Errorf("login after the host key rotation: %w", err)
	}
	if err := stage.Run(ctx, host[1:], h, b.opts()); err != nil {
		return err
	}
	if err := a.joinMesh(ctx, m.Name, b.cp, h); err != nil {
		return err
	}
	return b.pinMeshAddress(ctx, h.Addr)
}

// firstLogin logs in at the bootstrap address. The first time, the host key
// must have the fingerprint the operator read on the console, and is pinned;
// a run that resumes finds it pinned. sudo must work without a password.
func (b *byoBuild) firstLogin(ctx context.Context) (*stage.Host, error) {
	if b.bootstrap == "" {
		return nil, fmt.Errorf("%s has no key pinned at its mesh address %s yet, so the first login needs --bootstrap <its address> and --host-key <SHA256:…>, both read on its console",
			b.m.Name, b.m.Address)
	}
	if _, err := netip.ParseAddr(b.bootstrap); err != nil {
		return nil, fmt.Errorf("--bootstrap %q is not an IP address", b.bootstrap)
	}
	pinned, err := b.a.pins.Has(b.bootstrap)
	if err != nil {
		return nil, err
	}
	var seen ssh.PublicKey
	var cb ssh.HostKeyCallback
	if !pinned {
		if !fingerprint.MatchString(b.hostKey) {
			return nil, fmt.Errorf("--host-key %q is not a SHA256 fingerprint: on the console, ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub prints it", b.hostKey)
		}
		cb = remote.FingerprintHostKey(b.hostKey, &seen)
	}
	exec, err := b.dialBootstrap(ctx, cb)
	if err != nil {
		if remote.IsHostKeyError(err) && !pinned {
			return nil, fmt.Errorf("the machine at %s does not serve the host key %s: check the fingerprint on its console: %w", b.bootstrap, b.hostKey, err)
		}
		return nil, fmt.Errorf("first login to %s at %s: %w", b.m.Name, b.bootstrap, err)
	}
	h := &stage.Host{Name: b.m.Name, Addr: b.bootstrap, Exec: exec}
	if seen != nil {
		if err := b.a.pins.Set(b.bootstrap, seen); err != nil {
			closeExec(h)
			return nil, err
		}
		fmt.Fprintf(b.a.out, "[%s] pinned the host key the console showed (%s) at %s\n", b.m.Name, b.hostKey, b.bootstrap)
	}
	if _, err := exec.Run(ctx, "true"); err != nil {
		closeExec(h)
		user := b.a.cfg.Node.User
		return nil, fmt.Errorf("sudo needs a password for %[1]s on %[2]s; on its console run: echo '%[1]s ALL=(ALL) NOPASSWD:ALL' | sudo tee /etc/sudoers.d/%[1]s: %[3]w",
			user, b.m.Name, err)
	}
	fmt.Fprintf(b.a.out, "[%s] login as %s at %s verified against the host key\n", b.m.Name, b.a.cfg.Node.User, b.bootstrap)
	return h, nil
}

// dialBootstrap logs in at the bootstrap address as node.user with sudo,
// verified by cb, or by kluster's pins when cb is nil.
func (b *byoBuild) dialBootstrap(ctx context.Context, cb ssh.HostKeyCallback) (*remote.Client, error) {
	if cb == nil {
		var err error
		if cb, err = b.a.pins.Callback(); err != nil {
			return nil, err
		}
	}
	auth, err := remote.Auth(b.a.cfg.SSH.IdentityFile)
	if err != nil {
		return nil, err
	}
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return remote.Dial(wait, b.bootstrap, remote.Config{User: b.a.cfg.Node.User, Auth: auth, HostKeyCallback: cb, Sudo: true})
}

// pinMeshAddress proves the jump: the machine answers at its mesh address,
// through the Control Plane, with the key pinned at its bootstrap address,
// and only then is that key pinned at the mesh address.
func (b *byoBuild) pinMeshAddress(ctx context.Context, bootstrap string) error {
	keys, err := b.a.pins.Keys(bootstrap)
	if err != nil {
		return err
	}
	if len(keys) != 1 {
		return fmt.Errorf("%d keys are pinned at %s, want the rotated one alone", len(keys), bootstrap)
	}
	c, err := b.a.jumpClient(ctx, b.cp, b.m, remote.FixedHostKey(keys[0]))
	if err != nil {
		return fmt.Errorf("the jump through %s to %s at %s: %w", b.cp.Name, b.m.Name, b.m.Address, err)
	}
	_ = c.Close()
	if err := b.a.pins.Set(b.m.Address.String(), keys[0]); err != nil {
		return err
	}
	fmt.Fprintf(b.a.out, "[%s] answers through %s at %s with its key; pinned it there\n", b.m.Name, b.cp.Name, b.m.Address)
	return nil
}

// proveClosed shows that the site firewall holds: the jump still logs in,
// and the bootstrap address no longer takes SSH. Then the pin at the
// bootstrap address goes, because that address can belong to another device
// later.
func (b *byoBuild) proveClosed(ctx context.Context) error {
	c, err := b.a.jumpClient(ctx, b.cp, b.m, nil)
	if err != nil {
		return fmt.Errorf("after the site firewall, the jump to %s fails: on its console, systemctl stop kluster-site-firewall: %w", b.m.Name, err)
	}
	_ = c.Close()
	fmt.Fprintf(b.a.out, "[%s] the jump through %s still logs in\n", b.m.Name, b.cp.Name)
	if b.bootstrap == "" {
		fmt.Fprintf(b.a.out, "not checked that SSH at the bootstrap address is closed, and its pin stays: this run had no --bootstrap\n")
		return nil
	}
	if conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(b.bootstrap, "22")); err == nil {
		_ = conn.Close()
		return fmt.Errorf("SSH at %s still answers after the site firewall: the firewall does not hold", b.bootstrap)
	}
	fmt.Fprintf(b.a.out, "read back: SSH at %s no longer answers\n", b.bootstrap)
	if err := b.a.pins.Remove(b.bootstrap); err != nil {
		return err
	}
	fmt.Fprintf(b.a.out, "dropped the pin at %s\n", b.bootstrap)
	return nil
}

// planByo prints a byo build in Plan Mode.
func (a *app) planByo(ctx context.Context, m nodeset.Machine) error {
	fmt.Fprintf(a.out, "Plan Mode: --apply logs in to %s at --bootstrap with the --host-key fingerprint, or through %s once its key is pinned at %s, and runs:\n",
		m.Name, a.cfg.ControlPlane.Name, m.Address)
	c := a.cfg
	host := (&byoBuild{a: a, m: m}).hostStages()
	machine := slices.Concat([]stage.Stage{host[0], stages.RotateHostKey{}}, host[1:], meshJoinStages(false))
	if err := stage.PlanNew(ctx, machine, m.Name, a.out); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "then: write its mesh key to %s/%s, bring every joined machine's wg-mesh to its config, and pin its key at %s through %s\n",
		meshstore.Namespace, meshstore.Name, m.Address, c.ControlPlane.Name)
	if err := stage.PlanNew(ctx, []stage.Stage{stages.Join{Endpoint: c.ControlPlane.Endpoint}, stages.NodeRegistered{Node: m.Name, Timeout: byoRegisterTimeout}}, m.Name, a.out); err != nil {
		return err
	}
	ready := stages.NodeReady{Node: m.Name, InternalIP: m.Address.String(), ProviderID: m.ProviderID(), Timeout: nodeReadyTimeout}
	if err := stage.PlanNew(ctx, []stage.Stage{ready, stages.NodeProfile{Node: m.Name, Site: m.Site}}, c.ControlPlane.Name, a.out); err != nil {
		return err
	}
	if err := stage.PlanNew(ctx, []stage.Stage{stages.SiteFirewall{Public: m.Site.Reach == "public"}}, m.Name, a.out); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "then: log in through the jump again, check that SSH at the bootstrap address no longer answers, and drop its pin")
	return nil
}

// jumpHost is a session that can open a connection from its host, as `ssh
// -J` does.
type jumpHost interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// jumpClient logs in to a byo machine at its mesh address through the
// Control Plane's session, as node.user with sudo, verified by cb, or by
// kluster's pins when cb is nil.
func (a *app) jumpClient(ctx context.Context, cp *stage.Host, m nodeset.Machine, cb ssh.HostKeyCallback) (*remote.Client, error) {
	jump, ok := cp.Exec.(jumpHost)
	if !ok {
		return nil, fmt.Errorf("the session to %s cannot open a jump", cp.Name)
	}
	if cb == nil {
		var err error
		if cb, err = a.pins.Callback(); err != nil {
			return nil, err
		}
	}
	auth, err := remote.Auth(a.cfg.SSH.IdentityFile)
	if err != nil {
		return nil, err
	}
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return remote.DialVia(wait, jump.DialContext, m.Address.String(), remote.Config{User: a.cfg.Node.User, Auth: auth, HostKeyCallback: cb, Sudo: true})
}

// dialJump reaches a byo machine for a Stage: through the Control Plane, at
// its mesh address, verified only by kluster's pins.
func (a *app) dialJump(ctx context.Context, cp *stage.Host, m nodeset.Machine) (*stage.Host, func(), error) {
	if ok, err := a.pins.Has(m.Address.String()); err != nil {
		return nil, nil, err
	} else if !ok {
		return nil, nil, fmt.Errorf("no pin for %s at its mesh address %s: kluster node add %s builds it", m.Name, m.Address, m.Name)
	}
	c, err := a.jumpClient(ctx, cp, m, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("login to %s through %s: %w", m.Name, cp.Name, err)
	}
	fmt.Fprintf(a.out, "[%s] login as %s through %s at %s verified against the pinned host key\n", m.Name, a.cfg.Node.User, cp.Name, m.Address)
	return &stage.Host{Name: m.Name, Addr: m.Address.String(), Exec: c}, func() { _ = c.Close() }, nil
}

// closeExec closes a host's session when it has one.
func closeExec(h *stage.Host) {
	if c, ok := h.Exec.(*remote.Client); ok && c != nil {
		_ = c.Close()
	}
	h.Exec = nil
}

// writeBack writes updated over the file at path, keeping its mode, and
// reads it back.
func writeBack(path string, updated []byte, readBack func() error) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, updated, info.Mode().Perm()); err != nil {
		return err
	}
	return readBack()
}
