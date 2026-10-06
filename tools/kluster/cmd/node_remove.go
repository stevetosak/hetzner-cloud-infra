package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/spf13/cobra"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/filediff"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/intent"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/provision"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/sshgate"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stages"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/tf"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

func init() {
	nodeCmd.AddCommand(nodeRemoveCmd)
}

var nodeRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove one Worker: take it out of the cluster and the hub, then delete its server (docs/runbook/workers.md)",
	Long: "Run twice. While the environment's Worker set declares the name, kluster drains the Node " +
		"(PodDisruptionBudgets hold the drain, for 5 minutes at most), deletes it, removes the Worker's " +
		"peer from the hub without restarting the hub, drops the pin at its VPN address and, under " +
		"--apply, removes the entry from the set. kluster then stops, because it runs no Terraform on a " +
		"Worker set git does not hold: commit the edit and run again. The second run deletes exactly that " +
		"server through the workers Module, reads back that it is gone and drops its pin. A rehearsal " +
		"opens bootstrap SSH on the Control Plane and closes it at the end, failed runs included.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return run(cmd, func(ctx context.Context, a *app) error { return nodeRemove(ctx, a, args[0]) })
	},
}

// drainTimeout is how long node remove lets a drain wait on
// PodDisruptionBudgets.
const drainTimeout = 5 * time.Minute

func nodeRemove(ctx context.Context, a *app, name string) error {
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
	if w, declared := set.Get(name); declared {
		return a.retireWorker(ctx, set, w)
	}
	if srv != nil {
		return a.deleteWorkerServer(ctx, name, srv)
	}
	fmt.Fprintf(a.out, "%s declares no worker %s and the project has no server %s: nothing to remove\n", path, name, name)
	return nil
}

// retireWorker is run 1: the Worker leaves the cluster, the hub and the
// Worker set. Its server stays until run 2, so the Worker set git holds
// is the one Terraform deletes it from.
func (a *app) retireWorker(ctx context.Context, set *workerset.Set, w workerset.Worker) error {
	if err := workerset.CheckCommitted(ctx, set.Path); err != nil {
		return err
	}
	if err := notReservedVPN(a.cfg, w.VpnIP); err != nil {
		return err
	}
	updated, err := set.Without(w.Name)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s declares %s (private %s, VPN %s): run 1 of 2 takes it out of the cluster, the hub and the set\n",
		set.Path, w.Name, w.PrivateIP, w.VpnIP)

	if err := a.retireOnControlPlane(ctx, w); err != nil {
		return err
	}
	if a.mode.Apply {
		if err := a.pins.Remove(w.VpnIP); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "dropped the pin at %s's VPN address %s\n", w.Name, w.VpnIP)
	} else {
		fmt.Fprintf(a.out, "then: drop the pin at %s's VPN address %s\n", w.Name, w.VpnIP)
	}

	fmt.Fprint(a.out, filediff.Unified(set.Path, string(set.Bytes()), string(updated)))
	if !a.mode.Apply {
		fmt.Fprintln(a.out, "Plan Mode: --apply runs the Stages above and removes the entry")
		return nil
	}
	if err := writeSet(set.Path, updated, func(back *workerset.Set) bool {
		_, ok := back.Get(w.Name)
		return !ok
	}); err != nil {
		return fmt.Errorf("%w: it still declares %s", err, w.Name)
	}
	fmt.Fprintf(a.out, "read back: %s no longer declares %s\n", set.Path, w.Name)
	fmt.Fprintf(a.out, "commit %s, then run kluster node remove %s again: it deletes the server\n", set.Path, w.Name)
	return nil
}

// retireOnControlPlane drains and deletes the Worker's Node and removes its
// hub peer. A project with no Control Plane has neither.
func (a *app) retireOnControlPlane(ctx context.Context, w workerset.Worker) error {
	cpName := a.cfg.ControlPlane.Name
	cpSrv, err := a.api.Server(ctx, cpName)
	if err != nil {
		return err
	}
	if cpSrv == nil {
		fmt.Fprintf(a.out, "the project has no %s: no Node and no hub peer of %s to remove\n", cpName, w.Name)
		return nil
	}
	retire := retireStages(w)
	return a.onControlPlane(ctx, func(cp *stage.Host) error {
		if cp == nil {
			return stage.PlanNew(ctx, retire, cpName, a.out)
		}
		return stage.Run(ctx, retire, cp, stage.Options{Apply: a.mode.Apply, Out: a.out})
	})
}

// retireStages take a Worker out of the cluster and the hub, on the
// Control Plane.
func retireStages(w workerset.Worker) []stage.Stage {
	return []stage.Stage{
		stages.Drain{Node: w.Name, Timeout: drainTimeout},
		stages.DeleteNode{Node: w.Name},
		stages.HubPeerRemove{AllowedIPs: w.VpnIP + "/32", Now: time.Now},
	}
}

// deleteWorkerServer is run 2: the server of a Worker the set no longer
// declares is deleted through the workers Module. The Module's state is
// the proof that kluster's Terraform owns the server: a plan that does not
// delete exactly it is refused.
func (a *app) deleteWorkerServer(ctx context.Context, name string, srv *cloud.Server) error {
	// The commit gate runs before anything opens.
	workers, err := a.module(ctx, config.ModuleWorkers)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "the Worker set declares no %s, but its server exists at %s: run 2 of 2 deletes it\n", name, srv.PublicIP)
	cpSrv, err := a.api.Server(ctx, a.cfg.ControlPlane.Name)
	if err != nil {
		return err
	}
	if cpSrv != nil {
		if err := a.onControlPlane(ctx, func(cp *stage.Host) error { return a.checkNodeGone(ctx, cp, name) }); err != nil {
			return err
		}
	}
	_, applied, err := tf.Converge(ctx, workers, nil, deleteIntent(name), a.mode, a.out)
	if err != nil {
		return err
	}
	if !a.mode.Apply {
		fmt.Fprintf(a.out, "Plan Mode: --apply deletes %s, reads back that it is gone and drops the pin at %s\n", name, srv.PublicIP)
		return nil
	}
	if !applied {
		return fmt.Errorf("terraform deleted nothing: the workers state does not hold %s", name)
	}
	gone, err := a.api.Server(ctx, name)
	if err != nil {
		return err
	}
	if gone != nil {
		return fmt.Errorf("terraform deleted %s, but the API still has it (id %s, %s)", name, gone.ID, gone.Status)
	}
	fmt.Fprintf(a.out, "read back: the project has no server %s\n", name)
	if err := a.pins.Remove(srv.PublicIP); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "dropped the pin at %s\n", srv.PublicIP)
	return nil
}

// deleteIntent is run 2's plan: one delete, of that server, and nothing
// else.
func deleteIntent(name string) intent.Intent {
	return intent.Intent{
		Description: "delete Worker " + name,
		Expectations: []intent.Expectation{{
			Address: provision.WorkerAddress(name), Actions: []intent.Action{intent.Delete}, Required: true,
		}},
	}
}

// checkNodeGone refuses to delete a server whose Node is still in the
// cluster.
func (a *app) checkNodeGone(ctx context.Context, cp *stage.Host, name string) error {
	if cp == nil {
		fmt.Fprintf(a.out, "Plan Mode: not checked that no Node %s exists; --apply checks it before deleting\n", name)
		return nil
	}
	nodes, err := kube.Kubectl{Exec: cp.Exec}.Nodes(ctx)
	if err != nil {
		return err
	}
	return nodeGone(nodes, name)
}

func nodeGone(nodes []kube.Node, name string) error {
	for _, n := range nodes {
		if n.Name == name {
			return fmt.Errorf("a Node %s exists: run 1 did not finish, or its kubelet registered it again. "+
				"On the Control Plane: kubectl drain %s --ignore-daemonsets --delete-emptydir-data, "+
				"kubectl delete node %s; then run again", name, name, name)
		}
	}
	return nil
}

// notReservedVPN refuses a Worker whose VPN address is the hub's, the
// probe's or an operator peer's: its hub peer is not a Worker's to remove.
func notReservedVPN(c *config.Config, vpnIP string) error {
	ip, err := netip.ParseAddr(vpnIP)
	if err != nil {
		return fmt.Errorf("vpn_ip %q: %w", vpnIP, err)
	}
	reserved, err := reservedVPN(c)
	if err != nil {
		return err
	}
	for _, r := range reserved {
		if r == ip {
			return fmt.Errorf("the Worker set gives %s, which kluster.yaml reserves for the hub, the probe or an operator peer: node remove refuses", vpnIP)
		}
	}
	return nil
}

// onControlPlane reaches the Control Plane and calls fn with it. A rehearsal
// opens bootstrap SSH on the Control Plane firewall first and closes it at
// the end, failed runs included; live goes through WireGuard and opens
// nothing. In Plan Mode fn may get a nil host: the Control Plane was not
// reached.
func (a *app) onControlPlane(ctx context.Context, fn func(cp *stage.Host) error) (err error) {
	if !a.env.IsLive() {
		g, gerr := a.gate(ctx)
		if gerr != nil {
			return gerr
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
	}
	cp, closeCP, err := a.reachControlPlane(ctx)
	if err != nil {
		return err
	}
	defer closeCP()
	return fn(cp)
}
