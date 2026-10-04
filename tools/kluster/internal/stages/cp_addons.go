package stages

import (
	"context"
	"fmt"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// manifestDir is where the Control Plane Stages put the manifests they apply.
const manifestDir = "/var/lib/kluster/manifests"

// writeManifest puts manifest on the host for the Stage script to apply.
func writeManifest(ctx context.Context, h *stage.Host, name, manifest string) error {
	if _, err := h.Exec.Run(ctx, "mkdir -p -m 700 "+manifestDir); err != nil {
		return err
	}
	return h.Exec.WriteFile(ctx, manifestDir+"/"+name+".yaml", manifest, 0o600)
}

// Flannel is the pod network. The CNI is its own Stage so that Cilium can
// replace Flannel later as one Stage for another (ADR 0001).
type Flannel struct {
	Manifest  string // core/cni/flannel.yaml
	Node      string // k8s-cp
	PrivateIP string
	Interface string // enp7s0: what --iface-regex must select (ADR 0006)
	PodMTU    int    // what pods get; the manifest carries the underlay MTU
}

func (Flannel) Name() string    { return "cni-flannel" }
func (Flannel) Runbook() string { return "docs/runbook/control-plane.md#7-flannel" }

func (f Flannel) script() string {
	return adminKubectl + `kubectl apply -f ` + manifestDir + `/flannel.yaml
kubectl wait --for=condition=Ready node/` + f.Node + ` --timeout=300s
kubectl -n kube-flannel rollout status ds/kube-flannel-ds --timeout=300s
for i in $(seq 60); do ip link show flannel.1 >/dev/null 2>&1 && break; sleep 2; done
`
}

func (f Flannel) checks() []check {
	return []check{
		{"node-ready", adminKubectl + fmt.Sprintf(`[ "$(kubectl get node %s -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = True ]`, f.Node)},
		{"flannel-ds", adminKubectl + "kubectl -n kube-flannel rollout status ds/kube-flannel-ds --timeout=5s"},
		{"interface-pin", fmt.Sprintf("ip -d link show flannel.1 | grep -q 'local %s dev %s'", f.PrivateIP, f.Interface)},
		{"mtu", fmt.Sprintf("ip link show flannel.1 | grep -qw 'mtu %d'", f.PodMTU)},
	}
}

func (f Flannel) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	return probe(ctx, h, f.checks())
}

func (f Flannel) Preview(context.Context, *stage.Host) (string, error) {
	return "# write core/cni/flannel.yaml to " + manifestDir + "/flannel.yaml\n" + f.script(), nil
}

func (f Flannel) Act(ctx context.Context, h *stage.Host) error {
	if err := writeManifest(ctx, h, "flannel", f.Manifest); err != nil {
		return err
	}
	_, err := runScript(ctx, h, f.Name(), f.script())
	return err
}

// CloudController creates the `hcloud` Secret and runs the hcloud
// cloud-controller-manager, which gives the node its providerID and clears
// the uninitialized taint. It must come before the CSI driver.
type CloudController struct {
	Token     string // the Hetzner token; written to the host's tmpfs, never shown
	Network   string // tosak-net: makes the CCM network-aware (ADR 0006)
	Manifest  string // core/hcloud/ccm.yaml
	Node      string
	ServerID  string // the providerID must name this server
	PrivateIP string
}

func (CloudController) Name() string { return "cloud-controller" }
func (CloudController) Runbook() string {
	return "docs/runbook/control-plane.md#8-hcloud-cloud-controller-manager-then-the-csi-driver"
}

// tokenFile is on /run, a tmpfs, and lives only for the one command.
const tokenFile = "/run/kluster/hcloud-token"

func (c CloudController) script() string {
	return adminKubectl + `trap 'rm -f ` + tokenFile + `' EXIT
kubectl -n kube-system create secret generic hcloud \
  --from-file=token=` + tokenFile + ` --from-literal=network=` + c.Network + ` \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f ` + manifestDir + `/ccm.yaml
kubectl -n kube-system rollout status deploy/hcloud-cloud-controller-manager --timeout=300s
for i in $(seq 90); do
  [ -n "$(kubectl get node ` + c.Node + ` -o jsonpath='{.spec.providerID}')" ] && break
  sleep 2
done
`
}

func (c CloudController) checks() []check {
	// adminKubectl is a line of its own, so it leads each check: a `!`
	// before it would negate the export, not the check.
	node := "kubectl get node " + c.Node + " -o jsonpath="
	return []check{
		{"secret-network", adminKubectl + `[ "$(kubectl -n kube-system get secret hcloud -o jsonpath='{.data.network}' | base64 -d)" = ` + c.Network + ` ]`},
		{"secret-token", adminKubectl + `[ -n "$(kubectl -n kube-system get secret hcloud -o jsonpath='{.data.token}')" ]`},
		{"ccm", adminKubectl + "kubectl -n kube-system rollout status deploy/hcloud-cloud-controller-manager --timeout=5s"},
		{"provider-id", adminKubectl + `[ "$(` + node + `'{.spec.providerID}')" = hcloud://` + c.ServerID + ` ]`},
		{"initialized", adminKubectl + `! ` + node + `'{.spec.taints[*].key}' | grep -q uninitialized`},
		{"internal-ip", adminKubectl + `[ "$(` + node + `'{.status.addresses[?(@.type=="InternalIP")].address}')" = ` + c.PrivateIP + ` ]`},
	}
}

func (c CloudController) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	return probe(ctx, h, c.checks())
}

func (c CloudController) Preview(context.Context, *stage.Host) (string, error) {
	return "# write the token of this environment to " + tokenFile + " (tmpfs, 0600)\n" +
		"# write core/hcloud/ccm.yaml to " + manifestDir + "/ccm.yaml\n" + c.script(), nil
}

func (c CloudController) Act(ctx context.Context, h *stage.Host) error {
	if c.Token == "" {
		return fmt.Errorf("no Hetzner token for the hcloud Secret")
	}
	if err := writeManifest(ctx, h, "ccm", c.Manifest); err != nil {
		return err
	}
	if _, err := h.Exec.Run(ctx, "mkdir -p -m 700 /run/kluster"); err != nil {
		return err
	}
	if err := h.Exec.WriteFile(ctx, tokenFile, c.Token, 0o600); err != nil {
		return err
	}
	_, err := runScript(ctx, h, c.Name(), c.script())
	return err
}

// CSI is the hcloud CSI driver. It makes hcloud-volumes the default
// StorageClass (ADR 0005). Its controller stays Pending until a Worker
// joins, by design; only the node DaemonSet is checked.
type CSI struct {
	Manifest string // core/hcloud/csi.yaml
}

func (CSI) Name() string    { return "csi" }
func (CSI) Runbook() string { return "docs/runbook/control-plane.md#8c-the-csi-driver" }

func (CSI) script() string {
	return adminKubectl + `kubectl apply -f ` + manifestDir + `/csi.yaml
kubectl -n kube-system rollout status ds/hcloud-csi-node --timeout=300s
`
}

func (CSI) checks() []check {
	return []check{
		{"default-storageclass", adminKubectl + `[ "$(kubectl get sc hcloud-volumes -o jsonpath='{.metadata.annotations.storageclass\.kubernetes\.io/is-default-class}')" = true ]`},
		{"csi-node", adminKubectl + "kubectl -n kube-system rollout status ds/hcloud-csi-node --timeout=5s"},
	}
}

func (c CSI) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	return probe(ctx, h, c.checks())
}

func (c CSI) Preview(context.Context, *stage.Host) (string, error) {
	return "# write core/hcloud/csi.yaml to " + manifestDir + "/csi.yaml\n" + c.script(), nil
}

func (c CSI) Act(ctx context.Context, h *stage.Host) error {
	if err := writeManifest(ctx, h, "csi", c.Manifest); err != nil {
		return err
	}
	_, err := runScript(ctx, h, c.Name(), c.script())
	return err
}
