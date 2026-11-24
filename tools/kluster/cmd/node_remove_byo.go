package cmd

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/filediff"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/mesh"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/meshstore"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/nodeset"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/remote"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stages"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

// removeFlags are node remove's flags. Each belongs to one provisioner.
type removeFlags struct {
	unreachable bool // byo
}

var nodeRemoveFlags removeFlags

// check refuses a flag of another provisioner than the machine's.
func (f removeFlags) check(kind string) error {
	if f.unreachable && kind != nodeset.ProvisionerByo {
		return fmt.Errorf("--unreachable: not a flag of provisioner %s", kind)
	}
	return nil
}

// The wait for a leaving machine's wg-mesh to go down after byo-leave's
// timer.
const (
	meshGoneTimeout = time.Minute
	meshGonePoll    = 5 * time.Second
)

// byoRemove takes a byo machine out in one run
// (docs/runbook/workers.md#take-a-bring-your-own-machine-out). It has no
// server and no Terraform state, so nothing waits for a commit: kluster
// writes the Node set last, and the operator commits it.
func byoRemove(ctx context.Context, a *app, name string) error {
	set, err := a.nodeSet()
	if err != nil {
		return err
	}
	m, _ := set.Mesh().Machine(name)
	if err := workerset.CheckCommitted(ctx, set.Path, a.out); err != nil {
		return err
	}
	updated, err := set.WithoutMachine(name)
	if err != nil {
		return err
	}
	return a.withMeshAccess(ctx, func() error {
		cp, closeCP, err := a.reachControlPlane(ctx)
		if err != nil {
			return err
		}
		defer closeCP()
		r := a.newByoRemoval(set, m, updated, cp, nodeRemoveFlags.unreachable)
		if cp == nil {
			return r.plan(ctx)
		}
		return r.run(ctx)
	})
}

// byoRemoval is one node remove of a byo machine. The functions are its
// reach into the cluster, the machine and the files, so a test can follow
// the order of its steps.
type byoRemoval struct {
	out         io.Writer
	apply       bool
	set         *nodeset.Set
	m           nodeset.Machine
	updated     []byte // the Node set without the machine
	cp          *stage.Host
	cpName      string
	unreachable bool

	runStages func(ctx context.Context, s []stage.Stage, h *stage.Host) error
	retirable func(ctx context.Context) error
	nodes     func(ctx context.Context) ([]kube.Node, error)
	machine   func(ctx context.Context) (*stage.Host, func(), error)
	waitGone  func(ctx context.Context) error
	store     meshstore.Store
	writeSet  func(updated []byte) (*nodeset.Set, error)
	dropPin   func(addr string) error
	converge  func(ctx context.Context, set *nodeset.Set) error
}

func (a *app) newByoRemoval(set *nodeset.Set, m nodeset.Machine, updated []byte, cp *stage.Host, unreachable bool) *byoRemoval {
	opts := stage.Options{Apply: a.mode.Apply, Out: a.out}
	r := &byoRemoval{
		out: a.out, apply: a.mode.Apply, set: set, m: m, updated: updated, cp: cp,
		cpName: a.cfg.ControlPlane.Name, unreachable: unreachable,
		runStages: func(ctx context.Context, s []stage.Stage, h *stage.Host) error { return stage.Run(ctx, s, h, opts) },
		dropPin:   a.pins.Remove,
		writeSet:  func(b []byte) (*nodeset.Set, error) { return writeNodeSetWithout(set.Path, b, m.Name) },
	}
	if cp == nil {
		return r
	}
	k := kube.Kubectl{Exec: cp.Exec}
	r.retirable = func(ctx context.Context) error { return a.checkRetirable(ctx, cp, m.Name) }
	r.nodes = k.Nodes
	r.machine = func(ctx context.Context) (*stage.Host, func(), error) { return a.dialJump(ctx, cp, m) }
	r.waitGone = func(ctx context.Context) error { return waitMeshGone(ctx, cp, m, a.out) }
	r.store = meshstore.ConfigMap{Client: k}
	r.converge = func(ctx context.Context, back *nodeset.Set) error {
		run, closeSessions, err := a.newMeshRun(ctx, back, cp)
		if err != nil {
			return err
		}
		defer closeSessions()
		return run.run(ctx, "")
	}
	return r
}

// run takes the machine out: the gates, then the machine leaves (with
// --unreachable only its Node goes), then its records.
func (r *byoRemoval) run(ctx context.Context) error {
	fmt.Fprintf(r.out, "%s declares byo machine %s (site %s, mesh address %s): node remove takes it out in one run\n",
		r.set.Path, r.m.Name, r.m.Site.Name, r.m.Address)
	if err := r.retirable(ctx); err != nil {
		return err
	}
	leave := r.leave
	if r.unreachable {
		leave = r.dropNode
	}
	if err := leave(ctx); err != nil {
		return err
	}
	return r.forget(ctx)
}

// leave drains and deletes the Node, then resets the machine and takes the
// site firewall and wg-mesh off it, over the jump. The machine is reached
// first, so a machine kluster cannot reach keeps its Node.
func (r *byoRemoval) leave(ctx context.Context) error {
	h, closeH, err := r.machine(ctx)
	if err != nil {
		return fmt.Errorf("%w\nnode remove has a machine it can log in to leave the cluster. --unreachable takes out a dead, powered-off or reinstalled one, "+
			"and finishes a run that stopped after %s left the mesh", err, r.m.Name)
	}
	err = r.runStages(ctx, nodeOutStages(r.m.Name), r.cp)
	if err == nil {
		err = r.runStages(ctx, []stage.Stage{stages.ByoReset{}, stages.ByoLeave{}}, h)
	}
	// The session runs over the wg-mesh that byo-leave takes down.
	closeH()
	if err != nil || !r.apply {
		return err
	}
	return r.waitGone(ctx)
}

// dropNode takes out the Node of a machine kluster cannot reach. A Ready
// Node is refused: its kubelet answers, so the fault is in the path, and the
// machine would go on running its containers. Otherwise the Node is cordoned
// and deleted without a drain, which would wait on pods no kubelet ends; the
// pod garbage collector deletes them.
func (r *byoRemoval) dropNode(ctx context.Context) error {
	nodes, err := r.nodes(ctx)
	if err != nil {
		return err
	}
	if err := notReadyNode(nodes, r.m.Name); err != nil {
		return err
	}
	return r.runStages(ctx, []stage.Stage{stages.Cordon{Node: r.m.Name}, stages.DeleteNode{Node: r.m.Name}}, r.cp)
}

func notReadyNode(nodes []kube.Node, name string) error {
	for _, n := range nodes {
		if n.Name == name && n.Ready {
			return fmt.Errorf("--unreachable refused: Node %s is Ready, so its kubelet answers and the machine runs. "+
				"Find why the jump through the Control Plane fails, then run node remove without --unreachable", name)
		}
	}
	return nil
}

// forget takes the machine out of the mesh's records and peers, in an order
// that a stopped run finishes with --unreachable: its key out of the store,
// its entry out of the Node set, its pin, then every other machine's
// wg-mesh.
func (r *byoRemoval) forget(ctx context.Context) error {
	name, addr := r.m.Name, r.m.Address.String()
	fmt.Fprint(r.out, filediff.Unified(r.set.Path, string(r.set.Bytes()), string(r.updated)))
	if !r.apply {
		fmt.Fprintf(r.out, "Plan Mode: --apply then removes the data key of %s from %s/%s, writes the entry above out of %s, "+
			"drops the pin at %s and brings every other machine's %s to its config\n",
			name, meshstore.Namespace, meshstore.Name, r.set.Path, addr, mesh.Interface)
		return nil
	}
	if err := r.store.Remove(ctx, name); err != nil {
		return fmt.Errorf("removing the data key of %s from %s/%s: %w", name, meshstore.Namespace, meshstore.Name, err)
	}
	fmt.Fprintf(r.out, "removed the data key of %s from %s/%s\n", name, meshstore.Namespace, meshstore.Name)
	back, err := r.writeSet(r.updated)
	if err != nil {
		return err
	}
	fmt.Fprintf(r.out, "read back: %s no longer declares %s\n", r.set.Path, name)
	if err := r.dropPin(addr); err != nil {
		return err
	}
	fmt.Fprintf(r.out, "dropped the pin at %s\n", addr)
	if err := r.converge(ctx, back); err != nil {
		return fmt.Errorf("%w\n%s is out of the cluster and its records, but the other machines' %s still need their config: "+
			"kluster node mesh --apply, then commit %s", err, name, mesh.Interface, r.set.Path)
	}
	fmt.Fprintf(r.out, "commit %s: it no longer declares %s\n", r.set.Path, name)
	return nil
}

// plan prints the removal when the Control Plane was not reached.
func (r *byoRemoval) plan(ctx context.Context) error {
	fmt.Fprintln(r.out, "Plan Mode: the Control Plane was not reached, so nothing is checked; --apply runs the database gate and the last-Worker check, then:")
	if r.unreachable {
		if err := stage.PlanNew(ctx, []stage.Stage{stages.Cordon{Node: r.m.Name}, stages.DeleteNode{Node: r.m.Name}}, r.cpName, r.out); err != nil {
			return err
		}
	} else {
		if err := stage.PlanNew(ctx, nodeOutStages(r.m.Name), r.cpName, r.out); err != nil {
			return err
		}
		if err := stage.PlanNew(ctx, []stage.Stage{stages.ByoReset{}, stages.ByoLeave{}}, r.m.Name, r.out); err != nil {
			return err
		}
		fmt.Fprintf(r.out, "then: wait until %s no longer answers at %s through %s\n", r.m.Name, r.m.Address, r.cpName)
	}
	return r.forget(ctx)
}

// writeNodeSetWithout writes the Node set without the machine and reads it
// back.
func writeNodeSetWithout(path string, updated []byte, name string) (*nodeset.Set, error) {
	var back *nodeset.Set
	err := writeBack(path, updated, func() error {
		var err error
		if back, err = nodeset.Load(path); err != nil {
			return err
		}
		if meshDeclares(back.Mesh(), name) {
			return fmt.Errorf("read back: %s still declares %s", path, name)
		}
		return nil
	})
	return back, err
}

// waitMeshGone proves that the machine left the mesh: through the Control
// Plane, which still holds its peer, its SSH port no longer answers at its
// mesh address.
func waitMeshGone(ctx context.Context, cp *stage.Host, m nodeset.Machine, out io.Writer) error {
	jump, ok := cp.Exec.(jumpHost)
	if !ok {
		return fmt.Errorf("the session to %s cannot open a jump", cp.Name)
	}
	addr := net.JoinHostPort(m.Address.String(), "22")
	if err := waitGone(ctx, jump.DialContext, addr, meshGonePoll, meshGoneTimeout); err != nil {
		return fmt.Errorf("%w: %s did not go down on %s; on its console: systemctl status %s.timer",
			err, mesh.Interface, m.Name, "kluster-mesh-down")
	}
	fmt.Fprintf(out, "read back: %s no longer answers at %s through %s\n", m.Name, addr, cp.Name)
	return nil
}

// waitGone waits until a dial to addr fails, each dial for at most every.
func waitGone(ctx context.Context, dial remote.DialFunc, addr string, every, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		dialCtx, cancel := context.WithTimeout(ctx, every)
		conn, err := dial(dialCtx, "tcp", addr)
		cancel()
		if err != nil {
			return ctx.Err()
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			return fmt.Errorf("%s still answers after %s", addr, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

// byoReplace rebuilds a byo machine in place, in one run and with no
// commit: its Node goes and the machine is reset as node remove does it,
// then it is built again over the jump as node add builds it. Its entry, its
// mesh key, its site firewall and its pins stay.
func byoReplace(ctx context.Context, a *app, name string) error {
	set, err := a.nodeSet()
	if err != nil {
		return err
	}
	m, _ := set.Mesh().Machine(name)
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
			fmt.Fprintln(a.out, "Plan Mode: the Control Plane was not reached, so nothing is checked; --apply runs the byo checks, the database gate and the last-Worker check, then:")
			if err := stage.PlanNew(ctx, nodeOutStages(name), a.cfg.ControlPlane.Name, a.out); err != nil {
				return err
			}
			if err := stage.PlanNew(ctx, []stage.Stage{stages.ByoReset{}}, name, a.out); err != nil {
				return err
			}
			return a.planByo(ctx, m)
		}
		return a.newByoRebuild(set, m, cp).run(ctx)
	})
}

// byoRebuild is one node replace of a byo machine, with its reach as
// functions, as byoRemoval.
type byoRebuild struct {
	apply bool
	m     nodeset.Machine
	cp    *stage.Host

	preflight func(ctx context.Context) (clusterCA string, err error)
	retirable func(ctx context.Context) error
	machine   func(ctx context.Context) (*stage.Host, func(), error)
	runStages func(ctx context.Context, s []stage.Stage, h *stage.Host) error
	planBuild func(ctx context.Context) error
	build     func(ctx context.Context, clusterCA string) error
}

func (a *app) newByoRebuild(set *nodeset.Set, m nodeset.Machine, cp *stage.Host) *byoRebuild {
	opts := stage.Options{Apply: a.mode.Apply, Out: a.out}
	return &byoRebuild{
		apply:     a.mode.Apply,
		m:         m,
		cp:        cp,
		preflight: func(ctx context.Context) (string, error) { return a.preflightByo(ctx, set, m, cp) },
		retirable: func(ctx context.Context) error { return a.checkRetirable(ctx, cp, m.Name) },
		machine:   func(ctx context.Context) (*stage.Host, func(), error) { return a.dialJump(ctx, cp, m) },
		runStages: func(ctx context.Context, s []stage.Stage, h *stage.Host) error { return stage.Run(ctx, s, h, opts) },
		planBuild: func(ctx context.Context) error { return a.planByo(ctx, m) },
		build: func(ctx context.Context, ca string) error {
			return (&byoBuild{a: a, m: m, cp: cp, ca: ca}).run(ctx)
		},
	}
}

// run refuses before anything changes, as node add and node remove do, then
// takes the Node out, resets the machine and builds it again.
func (r *byoRebuild) run(ctx context.Context) error {
	ca, err := r.preflight(ctx)
	if err != nil {
		return err
	}
	if err := r.retirable(ctx); err != nil {
		return err
	}
	h, closeH, err := r.machine(ctx)
	if err != nil {
		return fmt.Errorf("%w\nnode replace rebuilds a machine kluster can log in to. A dead or reinstalled one goes out with "+
			"kluster node remove %[2]s --unreachable and comes back with kluster node add %[2]s", err, r.m.Name)
	}
	err = r.runStages(ctx, nodeOutStages(r.m.Name), r.cp)
	if err == nil {
		err = r.runStages(ctx, []stage.Stage{stages.ByoReset{}}, h)
	}
	closeH()
	if err != nil {
		return err
	}
	if !r.apply {
		return r.planBuild(ctx)
	}
	return r.build(ctx, ca)
}
