package cluster

import (
	"context"
	"fmt"
)

// SSHRunner is the minimal SSH surface cluster operations need. It's
// satisfied structurally by *remote.Client — this package doesn't import
// remote to keep the dependency direction one-way (cmd wires them together).
type SSHRunner interface {
	Run(ctx context.Context, cmd string) (string, error)
}

// CreateJoinToken creates a fresh kubeadm bootstrap token on the control
// plane and returns the full `kubeadm join ...` command it prints.
// Replaces the inline `ssh root@$CONTROL_PLANE_WG_IP "kubeadm token create
// --print-join-command"` in bootstrap_workers.sh.
func CreateJoinToken(ctx context.Context, cp SSHRunner) (string, error) {
	out, err := cp.Run(ctx, "kubeadm token create --print-join-command")
	if err != nil {
		return "", fmt.Errorf("creating kubeadm join token: %w", err)
	}
	return out, nil
}

// LonghornPreflight runs longhornctl's install/check preflight against the
// cluster from the control plane, matching bootstrap_workers.sh's final
// step.
func LonghornPreflight(ctx context.Context, cp SSHRunner) (string, error) {
	out, err := cp.Run(ctx, "export KUBECONFIG=/etc/kubernetes/admin.conf && /root/longhornctl install preflight && /root/longhornctl check preflight")
	if err != nil {
		return out, fmt.Errorf("longhorn preflight: %w", err)
	}
	return out, nil
}
