package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stages"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

// untilFlannel stops node pod-mtu after flannel.1 is rebuilt on every Node,
// before any workload restarts: the mixed state a rehearsal proves.
const untilFlannel = "flannel"

var podMTUUntil string

func init() {
	nodePodMTUCmd.Flags().StringVar(&podMTUUntil, "until", "", "stop after a Stage group; \"flannel\" stops before the workload restarts")
	nodeCmd.AddCommand(nodePodMTUCmd)
}

var nodePodMTUCmd = &cobra.Command{
	Use:   "pod-mtu",
	Short: "Give every pod kluster.yaml's podMTU: Flannel's ConfigMap, flannel.1 on each Node, then each workload restarted (docs/runbook/workers.md)",
	Long: "Every Node of the cluster, one at a time. On the Control Plane kluster sets net-conf.json in the " +
		"kube-flannel-cfg ConfigMap from core/cni/flannel.yaml. On each Node it deletes flannel.1 and that " +
		"Node's Flannel pod, waits for the new tunnel at podMTU, then sets cni0. Back on the Control Plane " +
		"it restarts, one at a time, each workload with a pod not at podMTU (CloudNativePG Clusters last, " +
		"by a switchover) and proves the end state. kluster dials every Node first and refuses a Node it " +
		"has no login to before anything changes. --until flannel stops before the restarts. Live goes " +
		"through WireGuard and opens nothing; a rehearsal opens bootstrap SSH and closes it at the end.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if podMTUUntil != "" && podMTUUntil != untilFlannel {
			return fmt.Errorf("--until %q: the only stop is %q", podMTUUntil, untilFlannel)
		}
		return run(cmd, func(ctx context.Context, a *app) error { return nodePodMTU(ctx, a, podMTUUntil) })
	},
}

const (
	flannelWait = 2 * time.Minute
	rolloutWait = 5 * time.Minute
	proofWait   = 3 * time.Minute
)

// podMTUSteps are the pod MTU Stages by the host they run on: config on the
// Control Plane, flannel on each Node in order, restart on the Control
// Plane. restart is empty when the run stops after flannel.
type podMTUSteps struct {
	config  []stage.Stage
	flannel []nodeSteps
	restart []stage.Stage
}

type nodeSteps struct {
	node  string
	steps []stage.Stage
}

// podMTUStages builds the Stages for the Nodes in order. cp is the Control
// Plane's Exec, through which each Node's Flannel pod is deleted; nodes
// holds a login to every Node, for the survey of pod interfaces.
func podMTUStages(manifest string, podMTU int, order []string, cp stage.Exec, nodes map[string]stage.Exec, until string) podMTUSteps {
	s := podMTUSteps{config: []stage.Stage{stages.FlannelConfig{Manifest: manifest, PodMTU: podMTU}}}
	for _, n := range order {
		s.flannel = append(s.flannel, nodeSteps{node: n, steps: []stage.Stage{
			stages.FlannelMTU{Node: n, PodMTU: podMTU, CP: cp, Wait: flannelWait, Every: 3 * time.Second},
		}})
	}
	if until != untilFlannel {
		s.restart = []stage.Stage{
			stages.PodMTURestart{Nodes: nodes, PodMTU: podMTU, Wait: rolloutWait, Every: 5 * time.Second},
			stages.PodMTUProof{Nodes: nodes, PodMTU: podMTU, Wait: proofWait, Every: 5 * time.Second},
		}
	}
	return s
}

func nodePodMTU(ctx context.Context, a *app, until string) error {
	path, err := a.cfg.Path(a.cfg.Manifests.Flannel)
	if err != nil {
		return err
	}
	manifest, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !a.env.IsLive() {
		a.warnOpenSSH(ctx)
	}
	return a.withMeshAccess(ctx, func() error {
		if !a.env.IsLive() && !a.mode.Apply {
			return a.planPodMTU(ctx, string(manifest), until)
		}
		return a.podMTU(ctx, string(manifest), until)
	})
}

// planPodMTU prints the Stages of a rehearsal in Plan Mode, where bootstrap
// SSH stays closed and no host is read.
func (a *app) planPodMTU(ctx context.Context, manifest, until string) error {
	fmt.Fprintln(a.out, "Plan Mode: bootstrap SSH stays closed, so no host is read")
	cpName := a.cfg.ControlPlane.Name
	s := podMTUStages(manifest, a.cfg.Cluster.PodMTU, []string{"<each Node>"}, nil, nil, until)
	if err := stage.PlanNew(ctx, s.config, cpName, a.out); err != nil {
		return err
	}
	for _, n := range s.flannel {
		if err := stage.PlanNew(ctx, n.steps, n.node, a.out); err != nil {
			return err
		}
	}
	return stage.PlanNew(ctx, s.restart, cpName, a.out)
}

// podMTU reaches every Node first, so a Node without a login stops the run
// before anything changes, then runs the Stages in order.
func (a *app) podMTU(ctx context.Context, manifest, until string) error {
	cp, closeCP, err := a.dialControlPlane(ctx)
	if err != nil {
		return err
	}
	defer closeCP()
	nodes, err := kube.Kubectl{Exec: cp.Exec}.Nodes(ctx)
	if err != nil {
		return err
	}
	path, err := a.workerSetPath()
	if err != nil {
		return err
	}
	workers, err := workerset.Load(path)
	if err != nil {
		return err
	}
	names := make([]string, len(nodes))
	for i, n := range nodes {
		names[i] = n.Name
	}
	if err := loginsFor(names, a.cfg.ControlPlane.Name, workers, path); err != nil {
		return err
	}
	hosts := map[string]*stage.Host{cp.Name: cp}
	execs := map[string]stage.Exec{cp.Name: cp.Exec}
	for _, n := range names {
		if n == cp.Name {
			continue
		}
		w, _ := workers.Get(n)
		h, closeW, err := a.dialWorker(ctx, w)
		if err != nil {
			return err
		}
		defer closeW()
		hosts[n], execs[n] = h, h.Exec
	}
	s := podMTUStages(manifest, a.cfg.Cluster.PodMTU, names, cp.Exec, execs, until)
	opts := stage.Options{Apply: a.mode.Apply, Out: a.out}
	if err := stage.Run(ctx, s.config, cp, opts); err != nil {
		return err
	}
	for _, n := range s.flannel {
		if err := stage.Run(ctx, n.steps, hosts[n.node], opts); err != nil {
			return err
		}
	}
	if err := stage.Run(ctx, s.restart, cp, opts); err != nil {
		return err
	}
	if until == untilFlannel {
		fmt.Fprintln(a.out, "Stopped after flannel (--until flannel): pods keep their old MTU until a run without --until")
	}
	if !a.mode.Apply {
		fmt.Fprintln(a.out, "Plan Mode: --apply runs the Stages above")
	}
	return nil
}

// loginsFor refuses a Node that is neither the Control Plane nor in the
// Worker set: kluster cannot reach it, so it cannot rebuild its tunnel.
func loginsFor(nodes []string, controlPlane string, workers *workerset.Set, path string) error {
	for _, n := range nodes {
		if n == controlPlane {
			continue
		}
		if _, ok := workers.Get(n); !ok {
			return fmt.Errorf("the Node %s is not the Control Plane and not in the Worker set %s: kluster has no login to it, so nothing was changed", n, path)
		}
	}
	return nil
}
