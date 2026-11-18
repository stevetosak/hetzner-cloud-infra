package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/netip"
	"slices"

	"github.com/spf13/cobra"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/mesh"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/meshstore"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/nodeset"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stages"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

func init() {
	nodeCmd.AddCommand(nodeMeshCmd)
}

var nodeMeshCmd = &cobra.Command{
	Use:   "mesh [<name>]",
	Short: "Join the machines of the Node set's mesh and bring each wg-mesh to its config (docs/runbook/workers.md#join-the-mesh)",
	Long: "Every machine of the Node set's mesh section, or the named one, passes the join checks, makes its " +
		"own key and writes the public half to the ConfigMap " + meshstore.Namespace + "/" + meshstore.Name + ". " +
		"Then every machine that has joined gets its config, because a new key changes the peers of the others. " +
		"A Node set without a mesh section changes nothing. kluster logs in only to the Control Plane and the " +
		"Worker set, so a machine outside Hetzner is refused until the bring-your-own step. node add, node " +
		"replace, cp init and up run the same steps for the machine they build. Live goes through WireGuard and " +
		"opens nothing; a rehearsal opens bootstrap SSH and closes it at the end.",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		only := ""
		if len(args) == 1 {
			only = args[0]
		}
		return run(cmd, func(ctx context.Context, a *app) error { return nodeMesh(ctx, a, only) })
	},
}

func nodeMesh(ctx context.Context, a *app, only string) error {
	set, err := a.nodeSet()
	if err != nil {
		return err
	}
	if set.Mesh() == nil {
		fmt.Fprintf(a.out, "%s has no mesh section, so no machine takes part in a mesh\n", set.Path)
		return nil
	}
	if !a.env.IsLive() {
		a.warnOpenSSH(ctx)
	}
	return a.withMeshAccess(ctx, func() error {
		cp, closeCP, err := a.reachControlPlane(ctx)
		if err != nil {
			return err
		}
		defer closeCP()
		if cp == nil {
			fmt.Fprintln(a.out, "Plan Mode: the Control Plane was not reached, so the mesh is not read; --apply opens bootstrap SSH and reads it")
			return nil
		}
		r, closeSessions, err := a.newMeshRun(ctx, set, cp)
		if err != nil {
			return err
		}
		defer closeSessions()
		if err := r.run(ctx, only); err != nil {
			return err
		}
		if !a.mode.Apply {
			fmt.Fprintln(a.out, "Plan Mode: --apply runs the Stages above")
		}
		return nil
	})
}

// withMeshAccess makes every machine reachable: live has WireGuard, and a
// rehearsal logs in through bootstrap SSH on both firewalls.
func (a *app) withMeshAccess(ctx context.Context, fn func() error) error {
	if a.env.IsLive() {
		return fn()
	}
	return a.withBootstrapSSH(ctx, fn)
}

// joinMesh joins the machine a build just finished, when the Node set's mesh
// declares it. The build holds the sessions, so they are used again.
func (a *app) joinMesh(ctx context.Context, name string, cp, self *stage.Host) error {
	set, err := a.nodeSet()
	if err != nil {
		return err
	}
	if !meshDeclares(set.Mesh(), name) {
		return nil
	}
	r, closeSessions, err := a.newMeshRun(ctx, set, cp, self)
	if err != nil {
		return err
	}
	defer closeSessions()
	return r.run(ctx, name)
}

// planMeshJoin prints what joinMesh would do on a machine that does not exist
// yet. Its config needs its key, so only the steps before the key are shown.
func (a *app) planMeshJoin(ctx context.Context, name string) error {
	set, err := a.nodeSet()
	if err != nil {
		return err
	}
	if !meshDeclares(set.Mesh(), name) {
		return nil
	}
	if err := stage.PlanNew(ctx, meshJoinStages(), name, a.out); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "then: write the public key of %s to %s/%s and bring every joined machine's %s to its config\n",
		name, meshstore.Namespace, meshstore.Name, mesh.Interface)
	return nil
}

func meshDeclares(m *nodeset.Mesh, name string) bool {
	if m == nil {
		return false
	}
	_, declared := m.Machine(name)
	return declared
}

// meshJoinStages run on a machine before its key goes to the store. kluster
// logs in only to Hetzner servers, so the private interface is always exempt
// from the LAN check.
func meshJoinStages() []stage.Stage {
	return []stage.Stage{stages.MeshJoinCheck{Hcloud: true}, stages.MeshKey{}}
}

// newMeshRun reads what a run needs. The hosts in open are sessions the caller
// owns; the returned function closes only the sessions the run dialled.
func (a *app) newMeshRun(ctx context.Context, set *nodeset.Set, cp *stage.Host, open ...*stage.Host) (*meshRun, func(), error) {
	path, err := a.workerSetPath()
	if err != nil {
		return nil, nil, err
	}
	workers, err := workerset.Load(path)
	if err != nil {
		return nil, nil, err
	}
	servers, err := a.api.Servers(ctx)
	if err != nil {
		return nil, nil, err
	}
	sessions := &meshSessions{
		open: map[string]*stage.Host{cp.Name: cp},
		dial: func(ctx context.Context, name string) (*stage.Host, func(), error) {
			w, ok := workers.Get(name)
			if !ok {
				return nil, nil, fmt.Errorf("%s is not in the Worker set %s", name, path)
			}
			return a.dialWorker(ctx, w)
		},
	}
	for _, h := range open {
		sessions.open[h.Name] = h
	}
	opts := stage.Options{Apply: a.mode.Apply, Out: a.out}
	return &meshRun{
		inventory: meshInventory{
			mesh:         set.Mesh(),
			hcloud:       set.Hcloud(),
			controlPlane: a.cfg.ControlPlane.Name,
			workers:      workers.Names(),
			servers:      servers,
		},
		store:     meshstore.ConfigMap{Client: kube.Kubectl{Exec: cp.Exec}},
		host:      sessions.host,
		runStages: func(ctx context.Context, s []stage.Stage, h *stage.Host) error { return stage.Run(ctx, s, h, opts) },
		publicKey: stages.MeshPublicKey,
		apply:     a.mode.Apply,
		out:       a.out,
	}, sessions.close, nil
}

// meshSessions keeps one login per machine for the length of a run.
type meshSessions struct {
	open    map[string]*stage.Host
	dial    func(ctx context.Context, name string) (*stage.Host, func(), error)
	closers []func()
}

func (s *meshSessions) host(ctx context.Context, name string) (*stage.Host, error) {
	if h, ok := s.open[name]; ok {
		return h, nil
	}
	h, closeHost, err := s.dial(ctx, name)
	if err != nil {
		return nil, err
	}
	s.open[name] = h
	s.closers = append(s.closers, closeHost)
	return h, nil
}

func (s *meshSessions) close() {
	for _, closeHost := range s.closers {
		closeHost()
	}
}

// meshHost is a machine of the mesh and the Hetzner server behind it.
type meshHost struct {
	machine nodeset.Machine
	server  *cloud.Server
}

// meshInventory is what tells kluster which machines of the mesh it can log
// in to.
type meshInventory struct {
	mesh         *nodeset.Mesh
	hcloud       nodeset.Site
	controlPlane string
	workers      []string
	servers      []*cloud.Server
}

// sort splits the machines of the mesh into the ones kluster can log in to
// and the reason for each of the others, by machine name.
func (i meshInventory) sort() ([]meshHost, map[string]error) {
	var ready []meshHost
	refused := map[string]error{}
	for _, m := range i.mesh.Machines {
		srv, err := i.server(m)
		if err != nil {
			refused[m.Name] = err
			continue
		}
		ready = append(ready, meshHost{machine: m, server: srv})
	}
	return ready, refused
}

func (i meshInventory) server(m nodeset.Machine) (*cloud.Server, error) {
	if m.Site.Provider != nodeset.ProviderHcloud {
		return nil, fmt.Errorf("mesh machine %s stands on site %s, and kluster logs in only to Hetzner servers until the bring-your-own step", m.Name, m.Site.Name)
	}
	if m.Name != i.controlPlane && !slices.Contains(i.workers, m.Name) {
		return nil, fmt.Errorf("mesh machine %s is neither %s nor in the Worker set", m.Name, i.controlPlane)
	}
	at := slices.IndexFunc(i.servers, func(s *cloud.Server) bool { return s.Name == m.Name })
	if at < 0 {
		return nil, fmt.Errorf("mesh machine %s has no Hetzner server", m.Name)
	}
	if err := checkSite(i.servers[at], i.hcloud); err != nil {
		return nil, err
	}
	return i.servers[at], nil
}

// meshMembers joins the Node set, the store and the Hetzner API into the
// machines the renderer takes. A machine with no entry has not joined yet, so
// it is no peer of the others.
func meshMembers(ready []meshHost, entries map[string]meshstore.Entry) ([]mesh.Machine, error) {
	var members []mesh.Machine
	var errs []error
	for _, h := range ready {
		e, joined := entries[h.machine.Name]
		if !joined {
			continue
		}
		member, err := meshMember(h, e)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		members = append(members, member)
	}
	return members, errors.Join(errs...)
}

func meshMember(h meshHost, e meshstore.Entry) (mesh.Machine, error) {
	public, err := netip.ParseAddr(h.server.PublicIP)
	if err != nil {
		return mesh.Machine{}, fmt.Errorf("mesh machine %s: the public address: %w", h.machine.Name, err)
	}
	if len(h.server.PrivateIPs) != 1 {
		return mesh.Machine{}, fmt.Errorf("mesh machine %s has the private addresses %v, want exactly one", h.machine.Name, h.server.PrivateIPs)
	}
	private, err := netip.ParseAddr(h.server.PrivateIPs[0])
	if err != nil {
		return mesh.Machine{}, fmt.Errorf("mesh machine %s: the private address: %w", h.machine.Name, err)
	}
	return mesh.Machine{
		Name:      h.machine.Name,
		Site:      h.machine.Site.Name,
		Public:    h.machine.Site.Reach == "public",
		Address:   h.machine.Address,
		Endpoint:  public,
		PrivateIP: private,
		PublicKey: e.PublicKey,
	}, nil
}

// meshRun is one pass over the mesh: the joins, then every config.
type meshRun struct {
	inventory meshInventory
	store     meshstore.Store
	host      func(ctx context.Context, name string) (*stage.Host, error)
	runStages func(ctx context.Context, s []stage.Stage, h *stage.Host) error
	publicKey func(ctx context.Context, h *stage.Host) (string, error)
	apply     bool
	out       io.Writer
}

// run joins every machine, or the one named only, and then brings every
// joined machine to its config. A join that fails stops the run before any
// config changes. A machine that fails its config does not stop the others,
// because they still need the new peer.
func (r *meshRun) run(ctx context.Context, only string) error {
	ready, refused := r.inventory.sort()
	if only != "" {
		if !meshDeclares(r.inventory.mesh, only) {
			return fmt.Errorf("the Node set's mesh has no machine %s", only)
		}
		if err, bad := refused[only]; bad {
			return err
		}
	}
	entries, err := r.store.Entries(ctx)
	if err != nil {
		return err
	}
	if err := meshstore.Check(entries, r.inventory.mesh); err != nil {
		return err
	}
	for _, h := range ready {
		if only != "" && h.machine.Name != only {
			continue
		}
		if err := r.join(ctx, h, entries); err != nil {
			return err
		}
	}
	err = r.converge(ctx, ready, entries)
	return errors.Join(err, r.report(refused, only))
}

// report names the machines the run left out. Only a run over the whole mesh
// fails on them: a build of one machine does not answer for the others.
func (r *meshRun) report(refused map[string]error, only string) error {
	for _, name := range slices.Sorted(maps.Keys(refused)) {
		fmt.Fprintln(r.out, "refused:", refused[name])
	}
	if only == "" && len(refused) > 0 {
		return fmt.Errorf("node mesh: %d machine(s) refused; the others are above", len(refused))
	}
	return nil
}

// join runs the join Stages on one machine and writes its public key to the
// store, and to entries, when the store does not hold that key.
func (r *meshRun) join(ctx context.Context, h meshHost, entries map[string]meshstore.Entry) error {
	name := h.machine.Name
	host, err := r.host(ctx, name)
	if err != nil {
		if r.apply {
			return err
		}
		fmt.Fprintf(r.out, "Plan Mode: %s was not reached (%v); --apply logs in\n", name, err)
		return nil
	}
	if err := r.runStages(ctx, meshJoinStages(), host); err != nil {
		return err
	}
	key, err := r.publicKey(ctx, host)
	if err != nil {
		if r.apply {
			return fmt.Errorf("%s: reading the mesh public key: %w", name, err)
		}
		fmt.Fprintf(r.out, "Plan Mode: %s has no mesh key yet; --apply makes it and writes the public key to %s/%s\n", name, meshstore.Namespace, meshstore.Name)
		return nil
	}
	e := meshstore.Entry{Name: name, Site: h.machine.Site.Name, Address: h.machine.Address, PublicKey: key}
	if entries[name] == e {
		return nil
	}
	if !r.apply {
		fmt.Fprintf(r.out, "Plan Mode: --apply writes the public key of %s to %s/%s\n", name, meshstore.Namespace, meshstore.Name)
		entries[name] = e
		return nil
	}
	if err := r.store.Put(ctx, e); err != nil {
		return fmt.Errorf("%s: writing the public key: %w", name, err)
	}
	fmt.Fprintf(r.out, "[%s] wrote its public key to %s/%s\n", name, meshstore.Namespace, meshstore.Name)
	entries[name] = e
	return nil
}

// converge brings every joined machine to its config.
func (r *meshRun) converge(ctx context.Context, ready []meshHost, entries map[string]meshstore.Entry) error {
	members, err := meshMembers(ready, entries)
	if err != nil {
		return err
	}
	var errs []error
	for _, h := range ready {
		name := h.machine.Name
		if _, joined := entries[name]; !joined {
			fmt.Fprintf(r.out, "%s has not joined the mesh yet: kluster node mesh %s joins it\n", name, name)
			continue
		}
		errs = append(errs, r.configure(ctx, name, members))
	}
	return errors.Join(errs...)
}

// configure renders one machine's config and brings its wg-mesh to it.
func (r *meshRun) configure(ctx context.Context, name string, members []mesh.Machine) error {
	config, err := mesh.Render(name, r.inventory.mesh.Relay, members)
	if err != nil {
		return err
	}
	host, err := r.host(ctx, name)
	if err != nil {
		if r.apply {
			return err
		}
		fmt.Fprintf(r.out, "Plan Mode: %s was not reached (%v); --apply logs in\n", name, err)
		return nil
	}
	return r.runStages(ctx, []stage.Stage{stages.WireGuardMesh{Config: config}}, host)
}
