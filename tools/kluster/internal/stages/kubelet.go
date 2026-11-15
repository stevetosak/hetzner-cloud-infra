package stages

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/filediff"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kubeletcfg"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// kubeletRunbook is the Workers runbook's kubelet section.
const kubeletRunbook = "docs/runbook/workers.md#set-the-kubelet-reserve"

// The kubelet configuration kubeadm keeps for every node, and the file each
// node's kubelet reads.
const (
	kubeletCMNamespace = "kube-system"
	kubeletCMName      = "kubelet-config"
	kubeletCMKey       = "kubelet"
	kubeletConfigFile  = "/var/lib/kubelet/config.yaml"
)

// The kubelet Stages of one node run in this order: KubeletRoom and
// KubeletConfigMap on the Control Plane, KubeletConfigFile and
// KubeletRestart on the node, KubeletProof on the Control Plane. Want is
// kluster.yaml's `kubelet:` block (config.Kubelet.Fields).

// running reads the node's running kubelet configuration through the API
// server.
func running(ctx context.Context, cp stage.Exec, node string) (map[string]any, error) {
	raw, err := kube.Kubectl{Exec: cp}.Configz(ctx, node)
	if err != nil {
		return nil, err
	}
	return kubeletcfg.Configz(raw)
}

// differDetail is a Probe's answer from the fields that still differ.
func differDetail(where string, d []string) stage.Status {
	if len(d) == 0 {
		return stage.Status{Done: true, Detail: where + " holds the kubelet values"}
	}
	return stage.Status{Detail: "not yet: " + where + " differs in " + strings.Join(d, ", ")}
}

// KubeletRoom refuses a node whose pods request more memory than the node
// will offer once the kubelet runs Want. A restarted kubelet admits its
// pods again, and rejects with OutOfmemory those that no longer fit.
type KubeletRoom struct {
	Node string
	Want map[string]any
}

func (KubeletRoom) Name() string    { return "kubelet-room" }
func (KubeletRoom) Runbook() string { return kubeletRunbook }

// room is the node's memory requests and its allocatable under Want.
type room struct {
	requests, allocatable resource.Quantity
	largest               []string
}

func (r KubeletRoom) room(ctx context.Context, h *stage.Host) (room, bool, error) {
	cfg, err := running(ctx, h.Exec, r.Node)
	if err != nil {
		return room{}, false, err
	}
	d, err := kubeletcfg.Differ(cfg, r.Want)
	if err != nil || len(d) == 0 {
		return room{}, true, err
	}
	n, err := findNode(ctx, h, r.Node)
	if err != nil {
		return room{}, false, err
	}
	if n == nil {
		return room{}, false, fmt.Errorf("no Node %s", r.Node)
	}
	next, err := kubeletcfg.With(cfg, r.Want)
	if err != nil {
		return room{}, false, err
	}
	alloc, err := kubeletcfg.Allocatable(n.CapacityMemory, next)
	if err != nil {
		return room{}, false, err
	}
	pods, err := kube.Kubectl{Exec: h.Exec}.PodsOn(ctx, r.Node)
	if err != nil {
		return room{}, false, err
	}
	return room{requests: sumRequests(pods), allocatable: alloc, largest: largest(pods, 3)}, false, nil
}

// sumRequests adds the memory requests of the pods that still run.
func sumRequests(pods []kube.Pod) resource.Quantity {
	var sum resource.Quantity
	for _, p := range pods {
		if !p.Finished {
			sum.Add(p.MemoryRequest)
		}
	}
	return sum
}

// largest names the n running pods with the largest memory requests.
func largest(pods []kube.Pod, n int) []string {
	var run []kube.Pod
	for _, p := range pods {
		if !p.Finished {
			run = append(run, p)
		}
	}
	sort.SliceStable(run, func(i, j int) bool { return run[i].MemoryRequest.Cmp(run[j].MemoryRequest) > 0 })
	var names []string
	for _, p := range run[:min(n, len(run))] {
		names = append(names, fmt.Sprintf("%s/%s %s", p.Namespace, p.Name, mi(p.MemoryRequest)))
	}
	return names
}

// mi writes a memory quantity in whole MiB.
func mi(q resource.Quantity) string { return fmt.Sprintf("%dMi", q.Value()>>20) }

func (r KubeletRoom) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	rm, applied, err := r.room(ctx, h)
	if err != nil {
		return stage.Status{}, err
	}
	if applied {
		return stage.Status{Done: true, Detail: "the kubelet already runs the values"}, nil
	}
	if rm.requests.Cmp(rm.allocatable) > 0 {
		return stage.Status{Detail: fmt.Sprintf("not yet: requests %s are above the new allocatable %s", mi(rm.requests), mi(rm.allocatable))}, nil
	}
	return stage.Status{Done: true, Detail: fmt.Sprintf("requests %s fit the new allocatable %s", mi(rm.requests), mi(rm.allocatable))}, nil
}

func (r KubeletRoom) refusal(rm room) string {
	return fmt.Sprintf("%s pods request %s, above the %s it offers with the new values: move pods off it first (largest: %s)",
		r.Node, mi(rm.requests), mi(rm.allocatable), strings.Join(rm.largest, ", "))
}

func (r KubeletRoom) Preview(ctx context.Context, h *stage.Host) (string, error) {
	if h.Exec == nil {
		return "# refuses when the pods on " + r.Node + " request more memory than its allocatable with the new values\n", nil
	}
	rm, _, err := r.room(ctx, h)
	if err != nil {
		return "", err
	}
	return "# refuses: " + r.refusal(rm) + "\n", nil
}

// Act changes nothing: the room is made by hand, with the operator's yes.
func (r KubeletRoom) Act(ctx context.Context, h *stage.Host) error {
	rm, applied, err := r.room(ctx, h)
	if err != nil || applied || rm.requests.Cmp(rm.allocatable) <= 0 {
		return err
	}
	return errors.New(r.refusal(rm))
}

// KubeletConfigMap sets Want in the kubelet-config ConfigMap, on the Control
// Plane. kubeadm gives that configuration to a node at its join and at
// `kubeadm upgrade node`, so a new or replaced Worker gets the values too.
// Only the fields of Want change; every other line stays.
type KubeletConfigMap struct {
	Want map[string]any
}

func (KubeletConfigMap) Name() string    { return "kubelet-configmap" }
func (KubeletConfigMap) Runbook() string { return kubeletRunbook }

const kubeletCMPath = kubeletCMNamespace + "/" + kubeletCMName

func (c KubeletConfigMap) read(ctx context.Context, h *stage.Host) (string, error) {
	return kube.Kubectl{Exec: h.Exec}.ConfigMapData(ctx, kubeletCMNamespace, kubeletCMName, kubeletCMKey)
}

func (c KubeletConfigMap) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	doc, err := c.read(ctx, h)
	if err != nil {
		return stage.Status{}, err
	}
	cfg, err := kubeletcfg.Parse(doc)
	if err != nil {
		return stage.Status{}, err
	}
	d, err := kubeletcfg.Differ(cfg, c.Want)
	if err != nil {
		return stage.Status{}, err
	}
	return differDetail("ConfigMap "+kubeletCMPath, d), nil
}

func (c KubeletConfigMap) Preview(ctx context.Context, h *stage.Host) (string, error) {
	cmd := "kubectl -n " + kubeletCMNamespace + " patch configmap " + kubeletCMName + " --type merge (data." + kubeletCMKey +
		": " + strings.Join(sortedFields(c.Want), ", ") + ")\n"
	if h.Exec == nil {
		return cmd, nil
	}
	doc, err := c.read(ctx, h)
	if err != nil {
		return "", err
	}
	merged, err := kubeletcfg.Merge(doc, c.Want)
	if err != nil {
		return "", err
	}
	return cmd + filediff.Unified(kubeletCMPath, doc, merged), nil
}

func sortedFields(m map[string]any) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (c KubeletConfigMap) Act(ctx context.Context, h *stage.Host) error {
	doc, err := c.read(ctx, h)
	if err != nil {
		return err
	}
	merged, err := kubeletcfg.Merge(doc, c.Want)
	if err != nil {
		return err
	}
	return kube.Kubectl{Exec: h.Exec}.PatchConfigMapData(ctx, kubeletCMNamespace, kubeletCMName, kubeletCMKey, merged)
}

// KubeletConfigFile writes the node's kubelet file from the ConfigMap, on
// the node: `kubeadm upgrade node phase kubelet-config` keeps the node's own
// containerRuntimeEndpoint (instance-config.yaml). The old file is kept as a
// backup. The kubelet reads the file only at its start (KubeletRestart).
type KubeletConfigFile struct {
	Want map[string]any
	Now  func() time.Time
}

func (KubeletConfigFile) Name() string    { return "kubelet-config-file" }
func (KubeletConfigFile) Runbook() string { return kubeletRunbook }

// fileHolds reports the fields of want the node's kubelet file does not hold.
func fileHolds(ctx context.Context, h *stage.Host, want map[string]any) ([]string, error) {
	doc, err := h.Exec.ReadFile(ctx, kubeletConfigFile)
	if err != nil {
		return nil, err
	}
	cfg, err := kubeletcfg.Parse(doc)
	if err != nil {
		return nil, err
	}
	return kubeletcfg.Differ(cfg, want)
}

func (f KubeletConfigFile) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	d, err := fileHolds(ctx, h, f.Want)
	if err != nil {
		return stage.Status{}, err
	}
	return differDetail(kubeletConfigFile, d), nil
}

func (f KubeletConfigFile) script() string {
	bak := kubeletConfigFile + ".bak-kluster-" + f.Now().UTC().Format("20060102T150405Z")
	return "cp -p " + kubeletConfigFile + " " + bak + "\nkubeadm upgrade node phase kubelet-config\n"
}

func (f KubeletConfigFile) Preview(context.Context, *stage.Host) (string, error) {
	return f.script(), nil
}

func (f KubeletConfigFile) Act(ctx context.Context, h *stage.Host) error {
	_, err := runScript(ctx, h, "kubelet-config", f.script())
	return err
}

// KubeletRestart restarts the node's kubelet, so it runs the new file. Its
// Probe reads the running configuration through the Control Plane (CP),
// because the node's own kubelet answers configz only through the API
// server. Act waits for the restarted kubelet up to Wait.
type KubeletRestart struct {
	Node  string
	Want  map[string]any
	CP    stage.Exec
	Wait  time.Duration
	Every time.Duration
}

func (KubeletRestart) Name() string    { return "kubelet-restart" }
func (KubeletRestart) Runbook() string { return kubeletRunbook }

func (r KubeletRestart) Probe(ctx context.Context, _ *stage.Host) (stage.Status, error) {
	cfg, err := running(ctx, r.CP, r.Node)
	if err != nil {
		return stage.Status{}, err
	}
	d, err := kubeletcfg.Differ(cfg, r.Want)
	if err != nil {
		return stage.Status{}, err
	}
	return differDetail("the running kubelet", d), nil
}

func (KubeletRestart) Preview(context.Context, *stage.Host) (string, error) {
	return "systemctl restart kubelet\n", nil
}

// Act refuses a node whose file does not hold Want: a restart then would
// change nothing, or run a half-written file.
func (r KubeletRestart) Act(ctx context.Context, h *stage.Host) error {
	d, err := fileHolds(ctx, h, r.Want)
	if err != nil {
		return err
	}
	if len(d) > 0 {
		return fmt.Errorf("%s does not hold %s: kluster restarts the kubelet only on the new file", kubeletConfigFile, strings.Join(d, ", "))
	}
	if _, err := h.Exec.Run(ctx, "systemctl restart kubelet"); err != nil {
		return err
	}
	return waitFor(ctx, r.Wait, r.Every, func() (stage.Status, error) {
		st, err := r.Probe(ctx, h)
		if err != nil {
			// The API server cannot reach configz while the kubelet starts.
			return stage.Status{Detail: err.Error()}, nil
		}
		return st, nil
	})
}

// KubeletProof is the proof after a restart, on the Control Plane: the Node
// is Ready, its kubelet runs Want, its allocatable memory is capacity minus
// the reserves and the hard eviction threshold, and no pod on it was
// rejected at admission. Act only waits up to Wait for the kubelet's next
// status report.
type KubeletProof struct {
	Node  string
	Want  map[string]any
	Wait  time.Duration
	Every time.Duration
}

func (KubeletProof) Name() string    { return "kubelet-proof" }
func (KubeletProof) Runbook() string { return kubeletRunbook }

// rejected reports a pod the kubelet refused at admission.
func rejected(p kube.Pod) bool {
	return p.Phase == "Failed" && (strings.HasPrefix(p.Reason, "OutOf") || p.Reason == "UnexpectedAdmissionError")
}

func (p KubeletProof) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	n, err := findNode(ctx, h, p.Node)
	if err != nil {
		return stage.Status{}, err
	}
	if n == nil {
		return stage.Status{}, fmt.Errorf("no Node %s", p.Node)
	}
	cfg, err := running(ctx, h.Exec, p.Node)
	if err != nil {
		return stage.Status{}, err
	}
	var not []string
	if d, err := kubeletcfg.Differ(cfg, p.Want); err != nil {
		return stage.Status{}, err
	} else if len(d) > 0 {
		not = append(not, "the kubelet runs "+strings.Join(d, ", "))
	}
	if !n.Ready {
		not = append(not, "Ready")
	}
	want, err := kubeletcfg.Allocatable(n.CapacityMemory, cfg)
	if err != nil {
		return stage.Status{}, err
	}
	got, err := resource.ParseQuantity(n.AllocatableMemory)
	if err != nil {
		return stage.Status{}, fmt.Errorf("allocatable memory %q: %w", n.AllocatableMemory, err)
	}
	if got.Cmp(want) != 0 {
		not = append(not, fmt.Sprintf("allocatable %s is %s", mi(want), mi(got)))
	}
	pods, err := kube.Kubectl{Exec: h.Exec}.PodsOn(ctx, p.Node)
	if err != nil {
		return stage.Status{}, err
	}
	for _, pod := range pods {
		if rejected(pod) {
			not = append(not, fmt.Sprintf("no rejected pod (%s/%s %s)", pod.Namespace, pod.Name, pod.Reason))
		}
	}
	if len(not) > 0 {
		return stage.Status{Detail: "not yet: " + strings.Join(not, ", ")}, nil
	}
	return stage.Status{Done: true, Detail: fmt.Sprintf("Ready, allocatable %s, no rejected pod", mi(got))}, nil
}

func (p KubeletProof) Preview(context.Context, *stage.Host) (string, error) {
	return "# wait for Ready, allocatable = capacity - kubeReserved - systemReserved - evictionHard, no pod rejected at admission\n", nil
}

func (p KubeletProof) Act(ctx context.Context, h *stage.Host) error {
	return waitFor(ctx, p.Wait, p.Every, func() (stage.Status, error) { return p.Probe(ctx, h) })
}

// waitFor polls check every `every` until it is done or `wait` has passed.
// It returns nil at the timeout: the Stage's own Probe then reports what is
// not done.
func waitFor(ctx context.Context, wait, every time.Duration, check func() (stage.Status, error)) error {
	deadline := time.Now().Add(wait)
	for {
		st, err := check()
		if err != nil {
			return err
		}
		if st.Done || !time.Now().Before(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}
