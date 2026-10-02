package pipeline

import (
	"context"
	"fmt"
)

// ContainerdStage installs and configures containerd, runc, and the CNI
// plugins at the versions pinned in kluster.yaml.
// Ports infra/scripts/bootstrap/pipeline/bootstrap_node-3-containerd.sh.
type ContainerdStage struct{}

func (ContainerdStage) Name() string { return "containerd" }

func (ContainerdStage) Run(ctx context.Context, node *NodeContext, opts *RunOptions) error {
	v := node.Cfg.Versions

	installCmds := []string{
		fmt.Sprintf("wget -qO- https://github.com/containerd/containerd/releases/download/v%s/containerd-%s-linux-amd64.tar.gz | tar -C /usr/local -xz", v.Containerd, v.Containerd),
		"mkdir -p /usr/local/lib/systemd/system",
		"curl -fsSL https://raw.githubusercontent.com/containerd/containerd/main/containerd.service -o /usr/local/lib/systemd/system/containerd.service",
		"systemctl daemon-reload",
		"systemctl enable --now containerd",
	}
	for _, cmd := range installCmds {
		if err := runCmd(ctx, node, opts, cmd); err != nil {
			return fmt.Errorf("installing containerd: %w", err)
		}
	}
	check(opts, node, "containerd installed")

	configCmds := []string{
		"mkdir -p /etc/containerd",
		"containerd config default > /etc/containerd/config.toml",
		"sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml",
		"systemctl restart containerd",
	}
	for _, cmd := range configCmds {
		if err := runCmd(ctx, node, opts, cmd); err != nil {
			return fmt.Errorf("configuring containerd: %w", err)
		}
	}
	check(opts, node, "containerd configured")

	runcCmds := []string{
		fmt.Sprintf("wget -q https://github.com/opencontainers/runc/releases/download/v%s/runc.amd64", v.Runc),
		"install -m 755 runc.amd64 /usr/local/sbin/runc",
		"mkdir -p /opt/cni/bin",
		fmt.Sprintf("wget -q https://github.com/containernetworking/plugins/releases/download/v%s/cni-plugins-linux-amd64-v%s.tgz", v.CNIPlugins, v.CNIPlugins),
		fmt.Sprintf("tar -C /opt/cni/bin -xzvf cni-plugins-linux-amd64-v%s.tgz", v.CNIPlugins),
	}
	for _, cmd := range runcCmds {
		if err := runCmd(ctx, node, opts, cmd); err != nil {
			return fmt.Errorf("installing runc/cni: %w", err)
		}
	}
	check(opts, node, "runc & CNI installed")

	return nil
}
