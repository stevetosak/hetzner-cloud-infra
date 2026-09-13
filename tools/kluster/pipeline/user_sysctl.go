package pipeline

import (
	"context"
	"fmt"
)

// UserSysctlStage creates the OS user, applies the k8s sysctl settings,
// disables swap, and loads the kernel modules containerd/kubelet need.
// Ports infra/scripts/bootstrap/pipeline/bootstrap_node-1-user-sysctl.sh.
type UserSysctlStage struct{}

func (UserSysctlStage) Name() string { return "user-sysctl" }

func (UserSysctlStage) Run(ctx context.Context, node *NodeContext, opts *RunOptions) error {
	user := node.Cfg.Node.User

	cmds := []string{
		fmt.Sprintf("adduser --disabled-password --gecos '' %s", user),
		fmt.Sprintf("usermod -aG sudo %s", user),
		fmt.Sprintf("mkdir -p /home/%s/.ssh", user),
		fmt.Sprintf("cp /root/.ssh/authorized_keys /home/%s/.ssh/authorized_keys", user),
		fmt.Sprintf("chmod 600 /home/%s/.ssh/authorized_keys", user),
		fmt.Sprintf("chown -R %s:%s /home/%s/.ssh", user, user, user),
	}
	for _, cmd := range cmds {
		if err := runCmd(ctx, node, opts, cmd); err != nil {
			return fmt.Errorf("creating user: %w", err)
		}
	}
	check(opts, node, "User created and SSH key copied")

	sysctlConf := "net.bridge.bridge-nf-call-iptables  = 1\n" +
		"net.bridge.bridge-nf-call-ip6tables = 1\n" +
		"net.ipv4.ip_forward                 = 1\n"
	if err := writeFile(ctx, node, opts, "/etc/sysctl.d/k8s.conf", sysctlConf, 0o644); err != nil {
		return fmt.Errorf("writing sysctl config: %w", err)
	}
	if err := runCmd(ctx, node, opts, "sysctl --system"); err != nil {
		return fmt.Errorf("applying sysctl config: %w", err)
	}
	check(opts, node, "Sysctl values applied")

	if err := runCmd(ctx, node, opts, "swapoff -a"); err != nil {
		return fmt.Errorf("disabling swap: %w", err)
	}
	if err := runCmd(ctx, node, opts, "sed -i '/ swap / s/^/#/' /etc/fstab"); err != nil {
		return fmt.Errorf("commenting out swap in fstab: %w", err)
	}
	check(opts, node, "Swap disabled")

	modulesConf := "overlay\nbr_netfilter\n"
	if err := writeFile(ctx, node, opts, "/etc/modules-load.d/k8s.conf", modulesConf, 0o644); err != nil {
		return fmt.Errorf("writing kernel modules config: %w", err)
	}
	for _, mod := range []string{"overlay", "br_netfilter"} {
		if err := runCmd(ctx, node, opts, "modprobe "+mod); err != nil {
			return fmt.Errorf("loading module %s: %w", mod, err)
		}
	}
	check(opts, node, "Kernel modules loaded")

	return nil
}
