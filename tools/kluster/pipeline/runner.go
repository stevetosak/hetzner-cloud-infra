package pipeline

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"
)

// DefaultPipeline returns the five bootstrap stages in the order they must
// run on a node.
func DefaultPipeline() []Stage {
	return []Stage{
		UserSysctlStage{},
		WireGuardStage{},
		ContainerdStage{},
		KubernetesStage{},
		LonghornStage{},
	}
}

// RunOnNode runs every stage in order against a single node, stopping at
// the first failing stage.
func RunOnNode(ctx context.Context, stages []Stage, node *NodeContext, opts *RunOptions) error {
	for _, stage := range stages {
		opts.Logger.Info("stage starting", "node", node.Name, "stage", stage.Name())
		if err := stage.Run(ctx, node, opts); err != nil {
			return fmt.Errorf("stage %s on node %s: %w", stage.Name(), node.Name, err)
		}
		if opts.Interactive && !opts.DryRun {
			pause(node.Name)
		}
	}
	return nil
}

// RunOnNodes runs the full stage pipeline against every node in parallel,
// mirroring bootstrap_workers.sh's `bootstrap_node ... & / wait` fan-out. It
// returns the first error encountered, after every node has finished (or
// failed) independently — like the bash version's backgrounded subshells,
// one node failing must not abort the others mid-command. This
// deliberately uses a plain errgroup.Group (not errgroup.WithContext),
// whose derived context would cancel every other node's in-flight SSH
// session — SIGKILLing them mid-`kubeadm join` or mid-file-write — the
// instant the first node returns an error. Cancellation should only come
// from the caller's own ctx (e.g. an operator interrupt).
func RunOnNodes(ctx context.Context, stages []Stage, nodes []*NodeContext, opts *RunOptions) error {
	var g errgroup.Group
	for _, node := range nodes {
		node := node
		g.Go(func() error {
			return RunOnNode(ctx, stages, node, opts)
		})
	}
	return g.Wait()
}
