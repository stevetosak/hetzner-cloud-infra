package stages

import (
	"context"
	"strconv"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/mesh"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// byoOutRunbook is the Workers runbook's section on a byo machine that
// leaves or is rebuilt.
const byoOutRunbook = "docs/runbook/workers.md#take-a-bring-your-own-machine-out"

// What the pod network leaves on a machine after kubeadm reset: the CNI
// config and the IP allocations of the bridge plugin, Flannel's subnet file,
// and the two interfaces. kubeadm reset (v1.37.0,
// cmd/kubeadm/app/cmd/phases/reset/cleanupnode.go) stops the kubelet, removes
// the Kubernetes containers, /etc/kubernetes/pki, the kubeconfig files and
// /var/lib/kubelet, and leaves all of these.
var (
	podNetworkDirs  = []string{"/etc/cni/net.d", "/var/lib/cni", "/run/flannel"}
	podNetworkLinks = []string{"cni0", "flannel.1"}
)

// ByoReset undoes the join of a byo machine, so it can leave the cluster or
// join again as a new Node: kubeadm reset, then the pod network's leftovers.
// A new Node can get another pod subnet, and the bridge plugin refuses a cni0
// that holds the old one. The packages, the mesh and the site firewall stay.
// So do the iptables chains: kube-proxy and Flannel rewrite theirs at the
// next join.
type ByoReset struct{}

func (ByoReset) Name() string    { return "byo-reset" }
func (ByoReset) Runbook() string { return byoOutRunbook }

func (ByoReset) script() string {
	s := "if command -v kubeadm >/dev/null; then kubeadm reset -f; fi\nrm -rf"
	for _, d := range podNetworkDirs {
		s += " " + d
	}
	s += "\n"
	for _, l := range podNetworkLinks {
		s += "if ip link show " + l + " >/dev/null 2>&1; then ip link delete " + l + "; fi\n"
	}
	return s
}

func (ByoReset) checks() []check {
	c := []check{
		{"no-kubelet-conf", "[ ! -e /etc/kubernetes/kubelet.conf ]"},
		{"no-cluster-ca", "[ ! -e " + clusterCAFile + " ]"},
		{"kubelet-stopped", "! systemctl is-active --quiet kubelet"},
	}
	for _, d := range podNetworkDirs {
		c = append(c, check{"no-" + d, "[ ! -e " + d + " ]"})
	}
	for _, l := range podNetworkLinks {
		c = append(c, check{"no-" + l, "! ip link show " + l})
	}
	return c
}

func (r ByoReset) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	return probe(ctx, h, r.checks())
}

func (r ByoReset) Preview(context.Context, *stage.Host) (string, error) { return r.script(), nil }

func (r ByoReset) Act(ctx context.Context, h *stage.Host) error {
	_, err := runScript(ctx, h, r.Name(), r.script())
	return err
}

// The transient timer that takes wg-mesh down after ByoLeave, and its delay:
// long enough for the Stage's Probe to run over the session that wg-mesh
// carries.
const (
	meshDownUnit  = "kluster-mesh-down"
	meshDownDelay = 10 // seconds
)

// ByoLeave takes kluster's network parts off a byo machine that leaves the
// cluster: the site firewall, and wg-mesh with its key. The machine then
// takes SSH at its own address again, as after its install, and can join
// another cluster with node add. The session to it runs over wg-mesh, so the
// interface goes last: a transient timer deletes it after meshDownDelay, and
// the Probe passes while that timer is pending.
type ByoLeave struct{}

func (ByoLeave) Name() string    { return "byo-leave" }
func (ByoLeave) Runbook() string { return byoOutRunbook }

func (ByoLeave) script() string {
	return `if [ -e ` + siteFirewallPath + ` ]; then systemctl disable --now ` + siteFirewallUnit + `; fi
if nft list table inet ` + siteFirewallTable + ` >/dev/null 2>&1; then nft delete table inet ` + siteFirewallTable + `; fi
rm -f ` + siteFirewallFile + ` ` + siteFirewallPath + `
systemctl daemon-reload

if systemctl is-enabled --quiet ` + meshUnit + `; then systemctl disable ` + meshUnit + `; fi
rm -f ` + meshConf + ` ` + meshKeyFile + ` ` + meshPubFile + `
if ip link show ` + mesh.Interface + ` >/dev/null 2>&1 && ! systemctl is-active --quiet ` + meshDownUnit + `.timer; then
  systemd-run --quiet --collect --unit=` + meshDownUnit + ` --on-active=` + strconv.Itoa(meshDownDelay) + `s --timer-property=AccuracySec=1s \
    /bin/sh -c 'ip link delete ` + mesh.Interface + `; systemctl stop ` + meshUnit + `; systemctl reset-failed ` + meshUnit + `; true'
fi
`
}

func (ByoLeave) checks() []check {
	return []check{
		{"no-site-firewall", "[ ! -e " + siteFirewallFile + " ] && [ ! -e " + siteFirewallPath + " ] && ! nft list table inet " + siteFirewallTable},
		{"mesh-disabled", "! systemctl is-enabled --quiet " + meshUnit},
		{"no-mesh-key", "[ ! -e " + meshConf + " ] && [ ! -e " + meshKeyFile + " ] && [ ! -e " + meshPubFile + " ]"},
		{"mesh-going-down", "! ip link show " + mesh.Interface + " || systemctl is-active --quiet " + meshDownUnit + ".timer"},
	}
}

func (l ByoLeave) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	return probe(ctx, h, l.checks())
}

func (l ByoLeave) Preview(context.Context, *stage.Host) (string, error) { return l.script(), nil }

func (l ByoLeave) Act(ctx context.Context, h *stage.Host) error {
	_, err := runScript(ctx, h, l.Name(), l.script())
	return err
}
