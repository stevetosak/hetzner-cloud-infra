package stages

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// KubeadmInit runs `kubeadm init` with the flags ADR 0006 fixes. A dry run
// goes first, and the API server certificate it would sign must name every
// route to it before the real init runs.
type KubeadmInit struct {
	Endpoint    string // k8s-cp.tosak.internal
	PrivateIP   string // the advertise address
	VpnIP       string // the operator route
	PublicIP    string // break-glass only; port 6443 stays closed
	PodCIDR     string
	ServiceCIDR string
}

func (KubeadmInit) Name() string    { return "kubeadm-init" }
func (KubeadmInit) Runbook() string { return "docs/runbook/control-plane.md#5-kubeadm-init" }

// initLog holds init's output, which prints a bootstrap token. It is
// root-only and removed after a good init; the token is never shown.
const initLog = "/root/kluster-kubeadm-init.log"

func (k KubeadmInit) flags() string {
	return fmt.Sprintf("--control-plane-endpoint=%s:6443 --apiserver-advertise-address=%s --pod-network-cidr=%s --service-cidr=%s --apiserver-cert-extra-sans=%s,%s",
		k.Endpoint, k.PrivateIP, k.PodCIDR, k.ServiceCIDR, k.VpnIP, k.PublicIP)
}

// dryRunLog holds the dry run's output, which prints a join command too.
const dryRunLog = "/root/kluster-kubeadm-dry-run.log"

// noToken shows the end of a kubeadm log without its join command.
func noToken(log string) string {
	return "grep -v -e 'kubeadm join' -e 'discovery-token' -e '--token' " + log + " | tail -n 30"
}

func (k KubeadmInit) dryRun() string {
	return `umask 077
if ! kubeadm init --dry-run ` + k.flags() + ` > ` + dryRunLog + ` 2>&1; then
  ` + noToken(dryRunLog) + `
  exit 1
fi
if ! grep 'apiserver serving cert is signed for' ` + dryRunLog + `; then
  echo 'the dry run printed no serving certificate line'
  ` + noToken(dryRunLog) + `
  exit 1
fi
rm -f ` + dryRunLog + `
`
}

func (k KubeadmInit) init() string {
	return `rm -rf /etc/kubernetes/tmp
umask 077
if ! kubeadm init ` + k.flags() + ` > ` + initLog + ` 2>&1; then
  ` + noToken(initLog) + `
  exit 1
fi
rm -f ` + initLog + `
`
}

// wantSANs are what the serving certificate must name: the Endpoint, the
// first service address, and the private, VPN and public addresses.
func (k KubeadmInit) wantSANs() ([]string, error) {
	pfx, err := netip.ParsePrefix(k.ServiceCIDR)
	if err != nil {
		return nil, fmt.Errorf("service CIDR: %w", err)
	}
	return []string{k.Endpoint, pfx.Addr().Next().String(), k.PrivateIP, k.VpnIP, k.PublicIP}, nil
}

func missingSANs(line string, want []string) []string {
	have := map[string]bool{}
	for _, f := range strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '[' || r == ']' }) {
		have[f] = true
	}
	var missing []string
	for _, w := range want {
		if !have[w] {
			missing = append(missing, w)
		}
	}
	return missing
}

func (k KubeadmInit) checks() []check {
	sans := "openssl x509 -in /etc/kubernetes/pki/apiserver.crt -noout -ext subjectAltName"
	return []check{
		{"admin-conf", "[ -s /etc/kubernetes/admin.conf ]"},
		{"readyz", adminKubectl + `[ "$(kubectl get --raw=/readyz)" = ok ]`},
		{"endpoint", adminKubectl + "kubectl -n kube-system get cm kubeadm-config -o jsonpath='{.data.ClusterConfiguration}' | grep -qx 'controlPlaneEndpoint: " + k.Endpoint + ":6443'"},
		{"pod-subnet", adminKubectl + "kubectl -n kube-system get cm kubeadm-config -o jsonpath='{.data.ClusterConfiguration}' | grep -qx '  podSubnet: " + k.PodCIDR + "'"},
		{"service-subnet", adminKubectl + "kubectl -n kube-system get cm kubeadm-config -o jsonpath='{.data.ClusterConfiguration}' | grep -qx '  serviceSubnet: " + k.ServiceCIDR + "'"},
		{"cert-sans", sans + " | grep -q 'IP Address:" + k.VpnIP + "' && " + sans + " | grep -q 'IP Address:" + k.PublicIP + "'"},
	}
}

func (k KubeadmInit) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	return probe(ctx, h, k.checks())
}

func (k KubeadmInit) Preview(context.Context, *stage.Host) (string, error) {
	return "# dry run; the serving certificate must name " + strings.Join(k.mustSANs(), ", ") + "\n" +
		k.dryRun() + k.init(), nil
}

func (k KubeadmInit) mustSANs() []string {
	s, err := k.wantSANs()
	if err != nil {
		return []string{err.Error()}
	}
	return s
}

func (k KubeadmInit) Act(ctx context.Context, h *stage.Host) error {
	want, err := k.wantSANs()
	if err != nil {
		return err
	}
	line, err := runScript(ctx, h, k.Name()+"-dry-run", k.dryRun())
	if err != nil {
		return fmt.Errorf("kubeadm init --dry-run: %w", err)
	}
	if missing := missingSANs(line, want); len(missing) > 0 {
		return fmt.Errorf("the dry run's serving certificate lacks %v: %s", missing, line)
	}
	_, err = runScript(ctx, h, k.Name(), k.init())
	return err
}
