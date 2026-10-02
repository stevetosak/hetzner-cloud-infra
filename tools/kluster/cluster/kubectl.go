// Package cluster wraps kubectl/kubeadm operations and control-plane
// WireGuard maintenance — the pieces of bootstrap_workers.sh and
// reset-nodes.sh that were either inline SSH one-liners or ad-hoc local
// kubectl calls.
package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Kubectl shells out to the local `kubectl` binary, assuming the operator's
// kubeconfig already points at the cluster (matches how kubectl is used
// today — from the admin laptop, over the WireGuard VPN).
type Kubectl struct {
	Bin string // defaults to "kubectl" if empty
}

func (k Kubectl) bin() string {
	if k.Bin == "" {
		return "kubectl"
	}
	return k.Bin
}

func (k Kubectl) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, k.bin(), args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("kubectl %s: %w (output: %s)", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

// DrainNode drains nodeName, mirroring the flags reset/removal flows need:
// ignore daemonsets, delete emptyDir data, bounded timeout. force skips pod
// termination grace periods and drains pods not managed by a controller
// (kubectl drain's own --force/--grace-period=0), for --force node removal.
// A timeout of 0 is NOT "skip immediately" — kubectl drain treats 0 as
// "no timeout" (wait forever) — so force uses a short bound instead.
func (k Kubectl) DrainNode(ctx context.Context, nodeName string, force bool) error {
	args := []string{"drain", nodeName, "--ignore-daemonsets", "--delete-emptydir-data"}
	if force {
		args = append(args, "--force", "--grace-period=0", "--timeout=30s")
	} else {
		args = append(args, "--timeout=120s")
	}
	_, err := k.run(ctx, args...)
	if err != nil {
		return fmt.Errorf("draining node %s: %w", nodeName, err)
	}
	return nil
}

// DeleteNode removes nodeName from the cluster's node list. It does not
// error if the node is already gone (mirrors reset-nodes.sh's `|| true`).
func (k Kubectl) DeleteNode(ctx context.Context, nodeName string) error {
	_, err := k.run(ctx, "delete", "node", nodeName)
	if err != nil && !strings.Contains(err.Error(), "NotFound") {
		return fmt.Errorf("deleting node %s: %w", nodeName, err)
	}
	return nil
}

// GetNodes returns the current node names and their Ready status.
func (k Kubectl) GetNodes(ctx context.Context) (map[string]bool, error) {
	out, err := k.run(ctx, "get", "nodes", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}

	var parsed struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return nil, fmt.Errorf("parsing kubectl get nodes output: %w", err)
	}

	result := make(map[string]bool, len(parsed.Items))
	for _, item := range parsed.Items {
		ready := false
		for _, c := range item.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				ready = true
			}
		}
		result[item.Metadata.Name] = ready
	}
	return result, nil
}
