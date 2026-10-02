package pipeline

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/config"
)

// fakeRunner records every command/write sent to it instead of touching the
// network, so stage ordering and payloads can be asserted directly.
type fakeRunner struct {
	mu       sync.Mutex
	commands []string
	writes   []fakeWrite
}

type fakeWrite struct {
	path    string
	content string
}

func (f *fakeRunner) Run(_ context.Context, cmd string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, cmd)

	switch {
	case strings.Contains(cmd, "ip -4 addr show"):
		return "inet 10.0.2.6/24 brd 10.0.2.255 scope global enp7s0", nil
	default:
		return "", nil
	}
}

func (f *fakeRunner) WriteFile(_ context.Context, path, content string, _ os.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, fakeWrite{path: path, content: content})
	return nil
}

func (f *fakeRunner) ReadFile(_ context.Context, path string) (string, error) {
	if strings.HasSuffix(path, "private.key") {
		return "fake-private-key", nil
	}
	return "", nil
}

func testConfig() *config.Config {
	return &config.Config{
		Node: config.Node{
			User:             "tosak",
			NetworkInterface: "enp7s0",
		},
		WireGuard: config.WireGuard{
			Subnet: "10.100.0.0/24",
			Port:   51820,
		},
		Versions: config.Versions{
			Containerd: "2.2.0",
			Runc:       "1.4.0",
			CNIPlugins: "1.9.0",
			Kubernetes: "v1.34",
		},
	}
}

func testNode(runner *fakeRunner) *NodeContext {
	return &NodeContext{
		Name:            "k8swk-test",
		PrivateIP:       "10.0.2.6",
		VpnIP:           "10.100.0.2",
		SSH:             runner,
		CPWgPublicKey:   "cp-pubkey",
		CPPublicIP:      "1.2.3.4",
		KubeJoinCommand: "kubeadm join 10.0.1.5:6443 --token abc --discovery-token-ca-cert-hash sha256:def",
		Cfg:             testConfig(),
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func indexOfSubstring(commands []string, substr string) int {
	for i, c := range commands {
		if strings.Contains(c, substr) {
			return i
		}
	}
	return -1
}

func TestRunOnNode_StageOrder(t *testing.T) {
	runner := &fakeRunner{}
	node := testNode(runner)
	opts := &RunOptions{Logger: discardLogger()}

	if err := RunOnNode(context.Background(), DefaultPipeline(), node, opts); err != nil {
		t.Fatalf("RunOnNode() error: %v", err)
	}

	userIdx := indexOfSubstring(runner.commands, "adduser")
	wgIdx := indexOfSubstring(runner.commands, "wg genkey")
	containerdIdx := indexOfSubstring(runner.commands, "containerd-2.2.0-linux-amd64")
	k8sIdx := indexOfSubstring(runner.commands, "apt-get install -y kubelet kubeadm kubectl")
	joinIdx := indexOfSubstring(runner.commands, "kubeadm join")
	longhornIdx := indexOfSubstring(runner.commands, "open-iscsi")

	for name, idx := range map[string]int{
		"adduser": userIdx, "wg genkey": wgIdx, "containerd install": containerdIdx,
		"k8s packages": k8sIdx, "kubeadm join": joinIdx, "open-iscsi": longhornIdx,
	} {
		if idx == -1 {
			t.Fatalf("expected command containing %q, not found in: %v", name, runner.commands)
		}
	}

	if !(userIdx < wgIdx && wgIdx < containerdIdx && containerdIdx < k8sIdx && k8sIdx < joinIdx && joinIdx < longhornIdx) {
		t.Errorf("stages ran out of order: adduser=%d wg=%d containerd=%d k8s=%d join=%d longhorn=%d",
			userIdx, wgIdx, containerdIdx, k8sIdx, joinIdx, longhornIdx)
	}
}

func TestRunOnNode_DryRunSkipsExecution(t *testing.T) {
	runner := &fakeRunner{}
	node := testNode(runner)
	opts := &RunOptions{Logger: discardLogger(), DryRun: true}

	if err := RunOnNode(context.Background(), DefaultPipeline(), node, opts); err != nil {
		t.Fatalf("RunOnNode() error: %v", err)
	}

	if len(runner.commands) != 0 {
		t.Errorf("dry-run issued %d commands, want 0: %v", len(runner.commands), runner.commands)
	}
	if len(runner.writes) != 0 {
		t.Errorf("dry-run issued %d file writes, want 0", len(runner.writes))
	}
}

func TestRunOnNode_StopsOnFirstFailure(t *testing.T) {
	runner := &fakeRunner{}
	node := testNode(runner)
	node.KubeJoinCommand = "" // KubernetesStage requires this; force a failure there
	opts := &RunOptions{Logger: discardLogger()}

	err := RunOnNode(context.Background(), DefaultPipeline(), node, opts)
	if err == nil {
		t.Fatal("expected error from missing KubeJoinCommand, got nil")
	}

	if idx := indexOfSubstring(runner.commands, "open-iscsi"); idx != -1 {
		t.Error("longhorn stage ran after an earlier stage failed; pipeline should stop at first error")
	}
	// containerd (stage 3) runs before kubernetes (stage 4) fails, so it must have executed.
	if idx := indexOfSubstring(runner.commands, "containerd-2.2.0-linux-amd64"); idx == -1 {
		t.Error("expected containerd stage to have run before the kubernetes stage failed")
	}
}

func TestRunOnNodes_ParallelAllSucceed(t *testing.T) {
	runners := []*fakeRunner{{}, {}, {}}
	nodes := make([]*NodeContext, len(runners))
	for i, r := range runners {
		n := testNode(r)
		n.Name = n.Name + string(rune('a'+i))
		nodes[i] = n
	}
	opts := &RunOptions{Logger: discardLogger()}

	if err := RunOnNodes(context.Background(), DefaultPipeline(), nodes, opts); err != nil {
		t.Fatalf("RunOnNodes() error: %v", err)
	}

	for i, r := range runners {
		if indexOfSubstring(r.commands, "adduser") == -1 {
			t.Errorf("node %d never ran stage 1", i)
		}
	}
}
