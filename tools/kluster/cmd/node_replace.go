package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/provision"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/tf"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

func init() {
	nodeCmd.AddCommand(nodeReplaceCmd)
}

var nodeReplaceCmd = &cobra.Command{
	Use:   "replace <name>",
	Short: "Replace one Worker: take it out, delete its server, create it again under the same name and build it (docs/runbook/workers.md)",
	Long: "One run. The Worker keeps its entry in the Worker set, so no commit is needed. kluster refuses " +
		"while a CNPG Cluster has an instance that is not ready, and while the Worker is the only Ready " +
		"one. It drains and deletes the Node, removes the hub peer, drops the Worker's pins, then has " +
		"Terraform replace exactly that server with a seeded host key, and builds and joins it as node " +
		"add does. Bootstrap SSH is closed at the end with an API readback, failed runs included. A build " +
		"that stops after the replacement is resumed by node add.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return run(cmd, func(ctx context.Context, a *app) error { return nodeReplace(ctx, a, args[0]) })
	},
}

func nodeReplace(ctx context.Context, a *app, name string) error {
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
	w, declared := set.Get(name)
	if !declared {
		return fmt.Errorf("%s declares no worker %s: node replace keeps a declared Worker; node add declares one, node remove deletes a server the set no longer declares", path, name)
	}
	srv, err := a.api.Server(ctx, name)
	if err != nil {
		return err
	}
	if srv == nil {
		return fmt.Errorf("the project has no server %s: node add builds it", name)
	}
	return a.withReplaceAccess(ctx, func(workers *tf.Module, cpSrv *cloud.Server, cp *stage.Host) error {
		return a.replaceWorker(ctx, workers, w, srv, cpSrv, cp)
	})
}

// withReplaceAccess is what a replacement needs before it starts: the
// workers Module (its commit gate runs before anything opens), a Control
// Plane, bootstrap SSH and a session on the Control Plane. In Plan Mode cp
// may be nil: the Control Plane was not reached.
func (a *app) withReplaceAccess(ctx context.Context, fn func(workers *tf.Module, cpSrv *cloud.Server, cp *stage.Host) error) error {
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
	return a.withBootstrapSSH(ctx, func() error {
		cp, closeCP, err := a.reachControlPlane(ctx)
		if err != nil {
			return err
		}
		defer closeCP()
		return fn(workers, cpSrv, cp)
	})
}

// replaceWorker takes one Worker out of the cluster and the hub, has
// Terraform replace its server, and builds the new one. The old Node is
// gone before the new server exists, so the new kubelet registers a fresh
// Node and inherits no taints or labels.
func (a *app) replaceWorker(ctx context.Context, workers *tf.Module, w workerset.Worker, srv, cpSrv *cloud.Server, cp *stage.Host) error {
	fmt.Fprintf(a.out, "replace %s: server %s at %s, private %s, VPN %s\n", w.Name, srv.ID, srv.PublicIP, w.PrivateIP, w.VpnIP)
	if err := notReservedVPN(a.cfg, w.VpnIP); err != nil {
		return err
	}
	if err := a.checkReplaceable(ctx, cp, w.Name); err != nil {
		return err
	}
	retire := retireStages(w)
	if cp == nil {
		if err := stage.PlanNew(ctx, retire, a.cfg.ControlPlane.Name, a.out); err != nil {
			return err
		}
	} else if err := stage.Run(ctx, retire, cp, stage.Options{Apply: a.mode.Apply, Out: a.out}); err != nil {
		return err
	}
	if err := a.dropPins(w.Name, w.VpnIP, srv.PublicIP); err != nil {
		return err
	}

	ip, err := provision.ReplaceWorker(ctx, workers, w.Name, a.pins, a.mode, a.out)
	if err != nil {
		return err
	}
	if !a.mode.Apply {
		return a.planNewWorker(ctx, w, cpSrv, cp)
	}
	if ip == "" {
		return fmt.Errorf("terraform replaced nothing: the workers state does not hold %s", w.Name)
	}
	if srv, err = a.api.Server(ctx, w.Name); err != nil {
		return err
	}
	if srv == nil {
		return fmt.Errorf("terraform replaced %s, but the API has no such server", w.Name)
	}
	if err := checkNewWorker(srv, w); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "read back: %s id %s, %s, public %s, private %v\n", srv.Name, srv.ID, srv.Status, srv.PublicIP, srv.PrivateIPs)
	if err := a.buildWorker(ctx, w, srv, cpSrv, cp); err != nil {
		return fmt.Errorf("%w\nthe server %s was replaced: kluster node add %s --apply resumes its build", err, w.Name, w.Name)
	}
	return nil
}

// dropPins removes the old server's pins: at its VPN address and at its
// public address. The new server's keys are pinned as it is built.
func (a *app) dropPins(name string, addrs ...string) error {
	for _, addr := range addrs {
		if !a.mode.Apply {
			fmt.Fprintf(a.out, "then: drop the pin of %s at %s\n", name, addr)
			continue
		}
		if err := a.pins.Remove(addr); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "dropped the pin of %s at %s\n", name, addr)
	}
	return nil
}

// checkReplaceable is the gate before a Worker leaves the cluster: every
// CNPG Cluster healthy, and another Worker Ready to take its pods.
func (a *app) checkReplaceable(ctx context.Context, cp *stage.Host, name string) error {
	if cp == nil {
		fmt.Fprintf(a.out, "Plan Mode: the database gate and the last-Worker check are not run; --apply runs them before %s leaves\n", name)
		return nil
	}
	k := kube.Kubectl{Exec: cp.Exec}
	dbs, err := k.Databases(ctx)
	if err != nil {
		return err
	}
	msg, err := databaseGate(dbs)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "database gate: %s\n", msg)
	nodes, err := k.Nodes(ctx)
	if err != nil {
		return err
	}
	return notLastReadyWorker(nodes, name)
}

// databaseGate passes when every CNPG Cluster has all its instances ready,
// and when there is no CNPG Cluster at all, which it says.
func databaseGate(dbs []kube.Database) (string, error) {
	if len(dbs) == 0 {
		return "no CNPG Cluster in the cluster: passes, nothing to wait for", nil
	}
	var ok, not []string
	for _, d := range dbs {
		s := fmt.Sprintf("%s/%s %d/%d ready", d.Namespace, d.Name, d.Ready, d.Instances)
		if d.Healthy() {
			ok = append(ok, s)
		} else {
			not = append(not, fmt.Sprintf("%s (%s)", s, d.Phase))
		}
	}
	if len(not) > 0 {
		return "", fmt.Errorf("database gate: %s: a Worker leaves only while every CNPG instance is ready", strings.Join(not, ", "))
	}
	return strings.Join(ok, ", "), nil
}

// notLastReadyWorker refuses to take out the only Ready Worker: its pods
// would have nowhere to go. A Worker whose Node is absent or not Ready
// serves nothing, so it may go.
func notLastReadyWorker(nodes []kube.Node, name string) error {
	serving := false
	for _, n := range nodes {
		if n.ControlPlane || !n.Ready {
			continue
		}
		if n.Name != name {
			return nil
		}
		serving = true
	}
	if serving {
		return fmt.Errorf("%s is the only Ready Worker: replacing it leaves the cluster with none; node add another first", name)
	}
	return nil
}

// waitDatabases waits until the database gate passes, for at most timeout.
func (a *app) waitDatabases(ctx context.Context, cp *stage.Host, every, timeout time.Duration) error {
	k := kube.Kubectl{Exec: cp.Exec}
	deadline := time.Now().Add(timeout)
	for {
		dbs, err := k.Databases(ctx)
		if err != nil {
			return err
		}
		msg, gateErr := databaseGate(dbs)
		if gateErr == nil {
			fmt.Fprintf(a.out, "database gate: %s\n", msg)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("after %s: %w", timeout, gateErr)
		}
		fmt.Fprintf(a.out, "waiting: %v\n", gateErr)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}
