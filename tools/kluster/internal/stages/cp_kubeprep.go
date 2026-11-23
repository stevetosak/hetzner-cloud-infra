package stages

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/filediff"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// KubePrep installs the Kubernetes packages and sets the things that must be
// right before `kubeadm init` or `kubeadm join` and are silent when wrong:
// the Endpoint in /etc/hosts and the kubelet's arguments.
type KubePrep struct {
	// Version is an exact release (v1.37.0). The apt channel is its minor;
	// the packages are pinned to the patch, so a Worker matches the Control
	// Plane it joins.
	Version    string
	Endpoint   string // k8s-cp.tosak.internal
	EndpointIP string // the Control Plane's private address, on every node
	Kubelet    KubeletArgs
	// HoldCNI holds kubernetes-cni too: on a Worker it owns /opt/cni/bin
	// (docs/runbook/workers.md, step 5).
	HoldCNI bool
}

// KubeletArgs are the kubelet's own flags, in /etc/default/kubelet. They hold
// what differs per node; kubeadm's kubelet-config ConfigMap holds the rest.
type KubeletArgs struct {
	// NodeIP is the node's InternalIP: the private address of a Hetzner
	// server, the mesh address of any other machine.
	NodeIP string
	// CloudProviderExternal waits for a cloud controller to initialize the
	// Node: set on a Hetzner server, which the hcloud controller manages.
	CloudProviderExternal bool
	// ProviderID is set by the kubelet itself on a machine no cloud
	// controller manages: kluster://<site>/<node> (ADR 0016).
	ProviderID string
	// Taints are registered with the Node, as key=value:Effect. A kubelet
	// cannot change its taints after registration, but sets these at it.
	Taints []string
	// Labels are registered with the Node, as key=value; a kubelet may set
	// only the keys NodeRestriction allows, such as node.kubernetes.io/*.
	Labels []string
}

// String is the file's one line. A Hetzner server's line is the one every
// live node already holds, so its Probe still passes.
func (a KubeletArgs) String() string {
	line := "KUBELET_EXTRA_ARGS=--node-ip=" + a.NodeIP
	if a.CloudProviderExternal {
		line += " --cloud-provider=external"
	}
	if a.ProviderID != "" {
		line += " --provider-id=" + a.ProviderID
	}
	if len(a.Taints) > 0 {
		line += " --register-with-taints=" + strings.Join(a.Taints, ",")
	}
	if len(a.Labels) > 0 {
		line += " --node-labels=" + strings.Join(a.Labels, ",")
	}
	return line
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

func (k KubePrep) kubeletArgs() string { return k.Kubelet.String() }

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

# kubelet: the node's own arguments
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
