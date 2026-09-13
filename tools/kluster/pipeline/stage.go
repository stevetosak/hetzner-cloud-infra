// Package pipeline implements the node bootstrap stages: user/sysctl setup,
// WireGuard, containerd, Kubernetes, and Longhorn prerequisites. Each stage
// is independently testable against a mock remote.Client.
package pipeline

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/config"
)

// Runner is the subset of remote.Client a stage needs. Stages depend on
// this interface, not the concrete SSH client, so tests can substitute a
// recording fake.
type Runner interface {
	Run(ctx context.Context, cmd string) (string, error)
	WriteFile(ctx context.Context, path, content string, mode os.FileMode) error
	ReadFile(ctx context.Context, path string) (string, error)
}

// Stage is one bootstrap phase, applied to a single node.
type Stage interface {
	Name() string
	Run(ctx context.Context, node *NodeContext, opts *RunOptions) error
}

// NodeContext carries everything a stage needs about the node it is
// bootstrapping and the cluster it is joining.
type NodeContext struct {
	Name            string
	PublicIP        string
	PrivateIP       string
	VpnIP           string
	SSH             Runner
	CPWgPublicKey   string
	CPPublicIP      string
	KubeJoinCommand string
	Cfg             *config.Config
}

// RunOptions controls how a stage executes.
type RunOptions struct {
	DryRun      bool
	Interactive bool
	Logger      *slog.Logger
}

// runCmd runs cmd on the node's SSH connection, honoring DryRun by only
// logging the command instead of executing it — mirrors bootstrap_utils.sh's
// run() but without the historical redirect-escapes-the-wrapper bug.
func runCmd(ctx context.Context, node *NodeContext, opts *RunOptions, cmd string) error {
	if opts.DryRun {
		opts.Logger.Info("[DRY-RUN]", "node", node.Name, "cmd", cmd)
		return nil
	}
	out, err := node.SSH.Run(ctx, cmd)
	if err != nil {
		return err
	}
	if out != "" {
		opts.Logger.Debug("command output", "node", node.Name, "cmd", cmd, "output", out)
	}
	return nil
}

// writeFile writes content to path on the node, honoring DryRun.
func writeFile(ctx context.Context, node *NodeContext, opts *RunOptions, path, content string, mode os.FileMode) error {
	if opts.DryRun {
		opts.Logger.Info("[DRY-RUN] write file", "node", node.Name, "path", path)
		return nil
	}
	return node.SSH.WriteFile(ctx, path, content, mode)
}

// check logs a stage checkpoint, mirroring bootstrap_utils.sh's check().
func check(opts *RunOptions, node *NodeContext, msg string) {
	opts.Logger.Info("✔ "+msg, "node", node.Name)
}

// pauseMu serializes interactive pauses across nodes bootstrapped in
// parallel, so prompts from concurrent goroutines don't interleave on the
// terminal.
var pauseMu sync.Mutex

// pause blocks for operator confirmation between stages, mirroring
// bootstrap_utils.sh's pause() (only called when --interactive is set).
func pause(nodeName string) {
	pauseMu.Lock()
	defer pauseMu.Unlock()
	fmt.Printf("\n[%s] Press ENTER to continue...", nodeName)
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	fmt.Println()
}
