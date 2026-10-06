package stages

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/filediff"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// KubePrep installs the Kubernetes packages and sets the three things that
// must be right before `kubeadm init` or `kubeadm join` and are silent when
// wrong: the Endpoint in /etc/hosts, the kubelet's private node IP, and the
// external cloud provider.
type KubePrep struct {
	// Version is an exact release (v1.37.0). The apt channel is its minor;
	// the packages are pinned to the patch, so a Worker matches the Control
	// Plane it joins.
	Version    string
	Endpoint   string // k8s-cp.tosak.internal
	EndpointIP string // the Control Plane's private address, on every node
	PrivateIP  string // this host's private address, the kubelet's node IP
	// HoldCNI holds kubernetes-cni too: on a Worker it owns /opt/cni/bin
	// (docs/runbook/workers.md, step 5).
	HoldCNI bool
}

func (KubePrep) Name() string    { return "kube-prep" }
func (KubePrep) Runbook() string { return "docs/runbook/control-plane.md#4-prepare-for-kubeadm-init" }

func (k KubePrep) hostsLine() string { return k.EndpointIP + "  " + k.Endpoint }

func (k KubePrep) held() []string {
	h := []string{"kubelet", "kubeadm", "kubectl"}
	if k.HoldCNI {
		h = append(h, "kubernetes-cni")
	}
	return h
}

func (k KubePrep) kubeletArgs() string {
	return "KUBELET_EXTRA_ARGS=--node-ip=" + k.PrivateIP + " --cloud-provider=external"
}

// pkgVersion is the apt version glob for the release: v1.37.0 → 1.37.0-*.
func (k KubePrep) pkgVersion() string { return strings.TrimPrefix(k.Version, "v") + "-*" }

func (k KubePrep) script() string {
	minor := semver.MajorMinor(k.Version)
	repo := "https://pkgs.k8s.io/core:/stable:/" + minor + "/deb/"
	pin := k.pkgVersion()
	return fmt.Sprintf(`# Kubernetes %[1]s packages
mkdir -p /etc/apt/keyrings
curl -fsSL %[2]sRelease.key | gpg --dearmor --yes -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] %[2]s /' \
  > /etc/apt/sources.list.d/kubernetes.list
apt-get -o DPkg::Lock::Timeout=300 update
apt-get -o DPkg::Lock::Timeout=300 install -y \
  kubelet='%[6]s' kubeadm='%[6]s' kubectl='%[6]s'
apt-mark hold %[5]s

# the Control Plane Endpoint (ADR 0006)
grep -qxF '%[3]s' /etc/hosts || echo '%[3]s' >> /etc/hosts

# kubelet: private address, and external cloud provider
echo '%[4]s' > /etc/default/kubelet
systemctl daemon-reexec
systemctl enable kubelet
`, minor, repo, k.hostsLine(), k.kubeletArgs(), strings.Join(k.held(), " "), pin)
}

func (k KubePrep) checks() []check {
	v := strings.ReplaceAll(k.Version, ".", `\.`)
	return []check{
		{"kubeadm", "kubeadm version -o short | grep -qx '" + v + "'"},
		{"kubelet", "kubelet --version | grep -qx 'Kubernetes " + v + "'"},
		{"kubectl", "kubectl version --client 2>/dev/null | grep -qx 'Client Version: " + v + "'"},
		{"held", fmt.Sprintf(`[ "$(apt-mark showhold | grep -cxE '%s')" = %d ]`, strings.Join(k.held(), "|"), len(k.held()))},
		{"endpoint", fmt.Sprintf(`[ "$(getent hosts %s | awk '{print $1}')" = %s ]`, k.Endpoint, k.EndpointIP)},
		{"kubelet-args", fmt.Sprintf(`[ "$(cat /etc/default/kubelet)" = '%s' ]`, k.kubeletArgs())},
		{"kubelet-enabled", "systemctl is-enabled --quiet kubelet"},
	}
}

func (k KubePrep) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	return probe(ctx, h, k.checks())
}

// Preview shows the commands, and on an existing host the two file edits as
// diffs against what the host holds now.
func (k KubePrep) Preview(ctx context.Context, h *stage.Host) (string, error) {
	if h.Exec == nil {
		return k.script(), nil
	}
	hosts, err := h.Exec.Run(ctx, "cat /etc/hosts")
	if err != nil {
		return "", err
	}
	// Run trims the final newline from both reads, so neither side has one.
	newHosts := hosts
	if !containsLine(hosts, k.hostsLine()) {
		newHosts = hosts + "\n" + k.hostsLine()
	}
	kubelet, err := h.Exec.Run(ctx, "cat /etc/default/kubelet 2>/dev/null || true")
	if err != nil {
		return "", err
	}
	return k.script() + filediff.Unified("/etc/hosts", hosts, newHosts) +
		filediff.Unified("/etc/default/kubelet", kubelet, k.kubeletArgs()), nil
}

func (k KubePrep) Act(ctx context.Context, h *stage.Host) error {
	_, err := runScript(ctx, h, k.Name(), k.script())
	return err
}

func containsLine(s, line string) bool {
	for _, l := range strings.Split(s, "\n") {
		if l == line {
			return true
		}
	}
	return false
}
