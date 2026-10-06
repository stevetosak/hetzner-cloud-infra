package stages

import (
	"context"
	"fmt"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// BaseHost is the Control Plane's base setup: the operator user, kernel
// modules and sysctl, no swap, time sync, containerd, runc and the CNI
// plugins. It runs as root, the only user a fresh Hetzner image has.
type BaseHost struct {
	User       string // cp-dev
	Containerd string // 2.2.0
	Runc       string // 1.4.0
	CNIPlugins string // 1.9.0
}

func (BaseHost) Name() string    { return "base-host" }
func (BaseHost) Runbook() string { return "docs/runbook/control-plane.md#2-base-host-setup" }

func (b BaseHost) script() string {
	u := b.User
	return fmt.Sprintf(`# first boot: cloud-init writes the host key and the kluster marker
cloud-init status --wait >/dev/null || true

# user
id -u %[1]s >/dev/null 2>&1 || adduser --disabled-password --gecos '' %[1]s
usermod -aG sudo %[1]s
install -d -m 700 -o %[1]s -g %[1]s /home/%[1]s/.ssh
install -m 600 -o %[1]s -g %[1]s /root/.ssh/authorized_keys /home/%[1]s/.ssh/authorized_keys
echo '%[1]s ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/%[1]s
chmod 440 /etc/sudoers.d/%[1]s
visudo -c

# kernel modules and sysctl
printf 'overlay\nbr_netfilter\n' > /etc/modules-load.d/k8s.conf
modprobe overlay
modprobe br_netfilter
%[5]ssysctl --system

# swap
swapoff -a
sed -i '/ swap / s/^#*/#/' /etc/fstab

# packages and time sync
apt-get -o DPkg::Lock::Timeout=300 update
apt-get -o DPkg::Lock::Timeout=300 install -y chrony curl gpg apt-transport-https ca-certificates wget
systemctl enable --now chrony

# containerd %[2]s
wget -qO- https://github.com/containerd/containerd/releases/download/v%[2]s/containerd-%[2]s-linux-amd64.tar.gz | tar -C /usr/local -xz
mkdir -p /usr/local/lib/systemd/system
curl -fsSL https://raw.githubusercontent.com/containerd/containerd/v%[2]s/containerd.service \
  -o /usr/local/lib/systemd/system/containerd.service
mkdir -p /etc/containerd
containerd config default > /etc/containerd/config.toml
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
systemctl daemon-reload
systemctl enable --now containerd

# runc %[3]s and CNI plugins %[4]s
cd /tmp
wget -q https://github.com/opencontainers/runc/releases/download/v%[3]s/runc.amd64
install -m 755 runc.amd64 /usr/local/sbin/runc
mkdir -p /opt/cni/bin
wget -q https://github.com/containernetworking/plugins/releases/download/v%[4]s/cni-plugins-linux-amd64-v%[4]s.tgz
tar -C /opt/cni/bin -xzf cni-plugins-linux-amd64-v%[4]s.tgz
`, u, b.Containerd, b.Runc, b.CNIPlugins, heredoc("/etc/sysctl.d/k8s.conf", `net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1`))
}

func (b BaseHost) checks() []check {
	u := b.User
	return []check{
		{"user", "id -u " + u},
		{"sudo", "sudo -u " + u + " sudo -n true"},
		{"authorized-keys", "cmp -s /root/.ssh/authorized_keys /home/" + u + "/.ssh/authorized_keys"},
		{"modules", "lsmod | grep -q '^overlay ' && lsmod | grep -q '^br_netfilter '"},
		{"sysctl", `[ "$(sysctl -n net.bridge.bridge-nf-call-iptables net.bridge.bridge-nf-call-ip6tables net.ipv4.ip_forward | tr -d '\n')" = 111 ]`},
		{"no-swap", `[ -z "$(swapon --show)" ] && ! grep -qE '^[^#].* swap ' /etc/fstab`},
		{"chrony", "systemctl is-active --quiet chrony"},
		{"containerd", "containerd --version | grep -qw v" + b.Containerd + " && systemctl is-active --quiet containerd && systemctl is-enabled --quiet containerd"},
		{"systemd-cgroup", "grep -q 'SystemdCgroup = true' /etc/containerd/config.toml"},
		{"runc", "runc --version | head -1 | grep -qw " + b.Runc},
		{"cni-plugins", "[ -x /opt/cni/bin/bridge ]"},
	}
}

func (b BaseHost) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	return probe(ctx, h, b.checks())
}

func (b BaseHost) Preview(context.Context, *stage.Host) (string, error) { return b.script(), nil }

func (b BaseHost) Act(ctx context.Context, h *stage.Host) error {
	_, err := runScript(ctx, h, b.Name(), b.script())
	return err
}
