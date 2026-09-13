package pipeline

import (
	"context"
	"fmt"
	"regexp"
)

// privateIPPattern matches the "inet <addr>" line in `ip -4 addr show`
// output, mirroring bootstrap_node-4-kubernetes.sh's
// `grep -oP '(?<=inet\s)\d+(\.\d+){3}'` (Go's RE2 has no lookbehind, so we
// capture the address in a group instead).
var privateIPPattern = regexp.MustCompile(`inet\s+((?:\d+\.){3}\d+)`)

// KubernetesStage installs kubelet/kubeadm/kubectl, configures the kubelet
// to advertise the node's private IP, and runs the kubeadm join command.
// Ports infra/scripts/bootstrap/pipeline/bootstrap_node-4-kubernetes.sh.
//
// Fixes a real bug in the bash version: `run echo "..." > /etc/default/kubelet`
// wrote the redirect outside the string passed to run()/eval, so it bypassed
// --dry-run and run()'s error handling. Here the file write and the join
// command both go through writeFile/runCmd like every other step.
type KubernetesStage struct{}

func (KubernetesStage) Name() string { return "kubernetes" }

func (KubernetesStage) Run(ctx context.Context, node *NodeContext, opts *RunOptions) error {
	if node.KubeJoinCommand == "" {
		return fmt.Errorf("kubernetes stage requires KubeJoinCommand set on NodeContext")
	}

	k8sVersion := node.Cfg.Versions.Kubernetes
	installCmds := []string{
		"apt-get update",
		"apt-get install -y apt-transport-https ca-certificates curl gpg chrony",
		"mkdir -p /etc/apt/keyrings",
		fmt.Sprintf("curl -fsSL https://pkgs.k8s.io/core:/stable:/%s/deb/Release.key | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg", k8sVersion),
		fmt.Sprintf("echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/%s/deb/ /' > /etc/apt/sources.list.d/kubernetes.list", k8sVersion),
		"apt-get update",
		"apt-get install -y kubelet kubeadm kubectl",
		"apt-mark hold kubelet kubeadm kubectl",
	}
	for _, cmd := range installCmds {
		if err := runCmd(ctx, node, opts, cmd); err != nil {
			return fmt.Errorf("installing kubernetes packages: %w", err)
		}
	}
	check(opts, node, "Kubernetes packages installed")

	privateIP, err := detectPrivateIP(ctx, node, opts)
	if err != nil {
		return err
	}

	kubeletDefaults := fmt.Sprintf("KUBELET_EXTRA_ARGS=--node-ip=%s --cloud-provider=external\n", privateIP)
	if err := writeFile(ctx, node, opts, "/etc/default/kubelet", kubeletDefaults, 0o644); err != nil {
		return fmt.Errorf("writing kubelet defaults: %w", err)
	}
	check(opts, node, fmt.Sprintf("kubelet will advertise IP: %s", privateIP))

	if err := runCmd(ctx, node, opts, "systemctl daemon-reexec"); err != nil {
		return fmt.Errorf("reexecuting systemd: %w", err)
	}
	if err := runCmd(ctx, node, opts, "systemctl enable kubelet"); err != nil {
		return fmt.Errorf("enabling kubelet: %w", err)
	}
	check(opts, node, "Worker ready for kubeadm join")

	if err := runCmd(ctx, node, opts, node.KubeJoinCommand); err != nil {
		return fmt.Errorf("running kubeadm join: %w", err)
	}
	check(opts, node, "Joined cluster")

	return nil
}

func detectPrivateIP(ctx context.Context, node *NodeContext, opts *RunOptions) (string, error) {
	if opts.DryRun {
		return node.PrivateIP, nil
	}

	iface := node.Cfg.Node.NetworkInterface
	out, err := node.SSH.Run(ctx, fmt.Sprintf("ip -4 addr show %s", iface))
	if err != nil {
		return "", fmt.Errorf("reading address on %s: %w", iface, err)
	}
	match := privateIPPattern.FindStringSubmatch(out)
	if match == nil {
		return "", fmt.Errorf("failed to detect IP on %s", iface)
	}
	return match[1], nil
}
