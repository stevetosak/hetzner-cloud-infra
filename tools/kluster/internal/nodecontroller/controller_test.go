package nodecontroller

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	corelisters "k8s.io/client-go/listers/core/v1"
	storagelisters "k8s.io/client-go/listers/storage/v1"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/deadnode"
)

var now = time.Date(2026, 11, 24, 20, 0, 0, 0, time.UTC)

type opt func(*corev1.Node)

func node(name string, ready bool, since time.Duration, opts ...opt) *corev1.Node {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name), CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour))},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: status, LastTransitionTime: metav1.NewTime(now.Add(-since))},
		}},
	}
	for _, o := range opts {
		o(n)
	}
	return n
}

func taint(key string, added time.Duration) opt {
	return func(n *corev1.Node) {
		t := corev1.Taint{Key: key, Effect: corev1.TaintEffectNoExecute}
		if added >= 0 {
			at := metav1.NewTime(now.Add(-added))
			t.TimeAdded = &at
		}
		n.Spec.Taints = append(n.Spec.Taints, t)
	}
}

func cpLabel(n *corev1.Node) { n.Labels = map[string]string{controlPlane: ""} }

func healthy() []*corev1.Node {
	return []*corev1.Node{
		node("k8s-cp", true, 48*time.Hour, cpLabel),
		node("k8swk1", true, 48*time.Hour), node("k8swk2", true, 48*time.Hour), node("k8swk3", true, 48*time.Hour),
	}
}

func va(name, nodeName string) *storagev1.VolumeAttachment {
	return &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       storagev1.VolumeAttachmentSpec{NodeName: nodeName, Attacher: "csi.hetzner.cloud"},
	}
}

func pod(name, nodeName string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "monitoring"}, Spec: corev1.PodSpec{NodeName: nodeName}}
}

type rig struct {
	c       *Controller
	client  *fake.Clientset
	events  *record.FakeRecorder
	metrics *Metrics
	nodeIdx cache.Indexer
}

// newRig puts the objects in the fake API and the same Nodes and
// VolumeAttachments in the listers' caches.
func newRig(t *testing.T, nodes []*corev1.Node, vas []*storagev1.VolumeAttachment, pods ...*corev1.Pod) *rig {
	t.Helper()
	var objs []runtime.Object
	nodeIdx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	vaIdx := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for _, n := range nodes {
		objs = append(objs, n.DeepCopy())
		if err := nodeIdx.Add(n); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range vas {
		objs = append(objs, v.DeepCopy())
		if err := vaIdx.Add(v); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range pods {
		objs = append(objs, p)
	}
	client := fake.NewClientset(objs...)
	events := record.NewFakeRecorder(20)
	m := NewMetrics()
	c := New(client, corelisters.NewNodeLister(nodeIdx), storagelisters.NewVolumeAttachmentLister(vaIdx),
		events, m, deadnode.Defaults, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.now = func() time.Time { return now }
	return &rig{c: c, client: client, events: events, metrics: m, nodeIdx: nodeIdx}
}

func (r *rig) pass(t *testing.T) {
	t.Helper()
	if err := r.c.Pass(context.Background()); err != nil {
		t.Fatalf("Pass: %v", err)
	}
}

func (r *rig) get(t *testing.T, name string) *corev1.Node {
	t.Helper()
	n, err := r.client.CoreV1().Nodes().Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return n
}

func (r *rig) gone(t *testing.T, name string) bool {
	t.Helper()
	_, err := r.client.CoreV1().Nodes().Get(context.Background(), name, metav1.GetOptions{})
	return apierrors.IsNotFound(err)
}

func (r *rig) drainEvents() []string {
	var out []string
	for {
		select {
		case e := <-r.events.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func TestPassTaintsADeadWorkerWithNoVolume(t *testing.T) {
	r := newRig(t, append(healthy(), node("ts1", false, 25*time.Minute)), nil)
	r.pass(t)

	n := r.get(t, "ts1")
	if len(n.Spec.Taints) != 1 {
		t.Fatalf("taints = %v, want the out-of-service taint", n.Spec.Taints)
	}
	got := n.Spec.Taints[0]
	if got.Key != deadnode.OutOfServiceTaint || got.Value != "nodeshutdown" || got.Effect != corev1.TaintEffectNoExecute ||
		got.TimeAdded == nil || !got.TimeAdded.Time.Equal(now) {
		t.Errorf("taint = %+v, want %s=nodeshutdown:NoExecute added at %s", got, deadnode.OutOfServiceTaint, now)
	}
	ev := r.drainEvents()
	if len(ev) != 1 || !strings.HasPrefix(ev[0], "Warning "+EventTainted) || !strings.Contains(ev[0], "no-volume") {
		t.Errorf("events = %q, want one %s Warning naming no-volume", ev, EventTainted)
	}
	if v := testutil.ToFloat64(r.metrics.actions.WithLabelValues("taint", "ts1", "no-volume")); v != 1 {
		t.Errorf("actions_total{taint,ts1,no-volume} = %v, want 1", v)
	}
	if r.gone(t, "ts1") {
		t.Error("the Node was deleted in the same pass as its taint")
	}
	if !r.c.LastPass().Equal(now) || testutil.ToFloat64(r.metrics.lastPass) != float64(now.Unix()) {
		t.Errorf("last pass = %v, want %v", r.c.LastPass(), now)
	}
}

func TestPassDeletesATaintedWorkerOnceItsPodsAreGone(t *testing.T) {
	r := newRig(t, append(healthy(), node("ts1", false, 27*time.Minute, taint(deadnode.OutOfServiceTaint, time.Minute))), nil)
	var selector string
	r.client.PrependReactor("list", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		selector = a.(k8stesting.ListAction).GetListRestrictions().Fields.String()
		return false, nil, nil
	})
	r.pass(t)

	if selector != "spec.nodeName=ts1" {
		t.Errorf("pods were listed with the field selector %q, want spec.nodeName=ts1", selector)
	}
	if !r.gone(t, "ts1") {
		t.Fatal("the drained Node was not deleted")
	}
	ev := r.drainEvents()
	if len(ev) != 1 || !strings.HasPrefix(ev[0], "Warning "+EventRemoved) || !strings.Contains(ev[0], "kluster node add ts1") {
		t.Errorf("events = %q, want one %s that names kluster node add ts1", ev, EventRemoved)
	}
	if v := testutil.ToFloat64(r.metrics.removed.WithLabelValues("ts1", "no-volume")); v != float64(now.Unix()) {
		t.Errorf("removed_timestamp_seconds{ts1} = %v, want %v", v, now.Unix())
	}
	for _, del := range r.client.Actions() {
		if d, ok := del.(k8stesting.DeleteAction); ok && d.GetResource().Resource == "nodes" {
			if p := d.GetDeleteOptions().Preconditions; p == nil || p.UID == nil || *p.UID != "uid-ts1" {
				t.Errorf("delete preconditions = %+v, want the UID uid-ts1", p)
			}
		}
	}
}

func TestPassWaitsForTheDrain(t *testing.T) {
	r := newRig(t, append(healthy(), node("ts1", false, 27*time.Minute, taint(deadnode.OutOfServiceTaint, time.Minute))), nil,
		pod("grafana-0", "ts1"))
	r.pass(t)
	if r.gone(t, "ts1") {
		t.Error("the Node was deleted while a pod was still bound to it within the drain wait")
	}
}

func TestPassHoldsAWorkerWithAVolume(t *testing.T) {
	r := newRig(t, append(healthy()[:3], node("k8swk3", false, time.Hour)), []*storagev1.VolumeAttachment{va("csi-1", "k8swk3")})
	r.pass(t)
	r.pass(t)

	if n := r.get(t, "k8swk3"); len(n.Spec.Taints) != 0 {
		t.Errorf("a Worker with a volume and no provider report got taints %v", n.Spec.Taints)
	}
	ev := r.drainEvents()
	if len(ev) != 1 || !strings.HasPrefix(ev[0], "Warning "+EventHeld) {
		t.Errorf("events over two passes = %q, want one %s Warning", ev, EventHeld)
	}
	if v := testutil.ToFloat64(r.metrics.held.WithLabelValues("k8swk3", "volume-attached")); v != 1 {
		t.Errorf("held{k8swk3,volume-attached} = %v, want 1", v)
	}
}

func TestPassTaintsAWorkerWithAVolumeOnceItsProviderReportsItOff(t *testing.T) {
	r := newRig(t, append(healthy()[:3], node("k8swk3", false, time.Hour, taint(deadnode.ShutdownTaint, -1))),
		[]*storagev1.VolumeAttachment{va("csi-1", "k8swk3")})
	r.pass(t)
	if !hasTaint(r.get(t, "k8swk3"), deadnode.OutOfServiceTaint) {
		t.Error("a Worker its provider reports off did not get the out-of-service taint")
	}
	if v := testutil.ToFloat64(r.metrics.actions.WithLabelValues("taint", "k8swk3", "provider-off")); v != 1 {
		t.Errorf("actions_total{taint,k8swk3,provider-off} = %v, want 1", v)
	}
}

func TestPassClearsAHoldThatEnded(t *testing.T) {
	r := newRig(t, []*corev1.Node{node("a", true, time.Hour), node("b", false, time.Hour), node("c", false, time.Hour)}, nil)
	r.pass(t)
	if v := testutil.ToFloat64(r.metrics.held.WithLabelValues("b", "mass-guard")); v != 1 {
		t.Fatalf("held{b,mass-guard} = %v, want 1", v)
	}
	// c comes back: the guard lets b go, and its hold is gone from the metric.
	c := node("c", true, 0)
	if err := r.nodeIdx.Update(c); err != nil {
		t.Fatal(err)
	}
	if _, err := r.client.CoreV1().Nodes().Update(context.Background(), c, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	r.pass(t)
	if n := testutil.CollectAndCount(r.metrics.held); n != 0 {
		t.Errorf("held has %d series after the hold ended, want 0", n)
	}
	if !hasTaint(r.get(t, "b"), deadnode.OutOfServiceTaint) {
		t.Error("b was not tainted once the guard let it go")
	}
}

func TestPassUntaintsAWorkerThatIsReadyAgain(t *testing.T) {
	n := node("ts1", true, time.Minute, taint("other", -1), taint(deadnode.OutOfServiceTaint, 3*time.Minute))
	r := newRig(t, append(healthy(), n), nil)
	r.pass(t)
	got := r.get(t, "ts1").Spec.Taints
	if len(got) != 1 || got[0].Key != "other" {
		t.Errorf("taints = %v, want only the other taint left", got)
	}
}

func TestPassNeverTouchesTheControlPlane(t *testing.T) {
	cpTaint := func(n *corev1.Node) {
		n.Spec.Taints = []corev1.Taint{{Key: controlPlane, Effect: corev1.TaintEffectNoSchedule}}
	}
	for name, mark := range map[string]opt{"by label": cpLabel, "by taint": cpTaint} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, []*corev1.Node{
				node("k8s-cp", false, 2*time.Hour, mark),
				node("k8swk1", true, time.Hour), node("k8swk2", true, time.Hour), node("k8swk3", true, time.Hour),
			}, nil)
			r.pass(t)
			if len(r.get(t, "k8s-cp").Spec.Taints) > 1 || len(r.drainEvents()) != 0 {
				t.Error("the Control Plane was acted on")
			}
		})
	}
}

// The cache can lag the API. A Node that turned Ready, or was replaced by a
// new one of the same name, since the cache saw it is left alone.
func TestPassRechecksTheNodeBeforeItActs(t *testing.T) {
	t.Run("Ready in the API", func(t *testing.T) {
		r := newRig(t, append(healthy(), node("ts1", false, time.Hour)), nil)
		if _, err := r.client.CoreV1().Nodes().Update(context.Background(), node("ts1", true, 0), metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		r.pass(t)
		if hasTaint(r.get(t, "ts1"), deadnode.OutOfServiceTaint) {
			t.Error("a Node Ready in the API was tainted")
		}
	})
	t.Run("replaced in the API", func(t *testing.T) {
		r := newRig(t, append(healthy(), node("ts1", false, time.Hour, taint(deadnode.OutOfServiceTaint, 10*time.Minute))), nil)
		fresh := node("ts1", false, time.Hour)
		fresh.UID = "uid-new"
		if err := r.client.CoreV1().Nodes().Delete(context.Background(), "ts1", metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.client.CoreV1().Nodes().Create(context.Background(), fresh, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		r.pass(t)
		if r.gone(t, "ts1") {
			t.Error("a new Node of the same name was deleted")
		}
	})
	t.Run("gone from the API", func(t *testing.T) {
		r := newRig(t, append(healthy(), node("ts1", false, time.Hour)), nil)
		if err := r.client.CoreV1().Nodes().Delete(context.Background(), "ts1", metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		r.pass(t)
	})
}

func TestRead(t *testing.T) {
	n := node("ts1", false, 30*time.Minute, taint(deadnode.ShutdownTaint, -1), taint(deadnode.OutOfServiceTaint, 2*time.Minute))
	n.Annotations = map[string]string{deadnode.PauseAnnotation: "2026-11-25T00:00:00Z"}
	got := Read(n, 2)
	want := deadnode.Node{Name: "ts1", Since: now.Add(-30 * time.Minute), ProviderOff: true,
		Tainted: now.Add(-2 * time.Minute), Pause: "2026-11-25T00:00:00Z", Volumes: 2}
	if got != want {
		t.Errorf("Read = %+v\nwant %+v", got, want)
	}

	// No Ready condition yet: the clock starts at the Node's creation.
	bare := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "new", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))}}
	if d := Read(bare, 0); d.Ready || !d.Since.Equal(now.Add(-time.Hour)) {
		t.Errorf("Read of a Node with no conditions = %+v, want not Ready since its creation", d)
	}

	// A taint added by hand with no time: the drain counts from Since.
	manual := node("m", false, 40*time.Minute, taint(deadnode.OutOfServiceTaint, -1))
	if d := Read(manual, 0); !d.Tainted.Equal(now.Add(-40 * time.Minute)) {
		t.Errorf("Tainted of a taint with no time = %v, want %v", d.Tainted, now.Add(-40*time.Minute))
	}
}

func TestHandler(t *testing.T) {
	m := NewMetrics()
	var synced bool
	var last time.Time
	srv := httptest.NewServer(Handler(m, func() bool { return synced }, func() time.Time { return last }, time.Minute))
	defer srv.Close()
	status := func(path string) int {
		t.Helper()
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	if got := status("/health/live"); got != http.StatusOK {
		t.Errorf("/health/live = %d before the sync, want 200", got)
	}
	if got := status("/health"); got != http.StatusServiceUnavailable {
		t.Errorf("/health = %d before the sync, want 503", got)
	}
	synced = true
	if got := status("/health"); got != http.StatusServiceUnavailable {
		t.Errorf("/health = %d before the first pass, want 503", got)
	}
	last = time.Now()
	if got := status("/health"); got != http.StatusOK {
		t.Errorf("/health = %d after a pass, want 200", got)
	}
	last = time.Now().Add(-2 * time.Minute)
	if got := status("/health"); got != http.StatusServiceUnavailable {
		t.Errorf("/health = %d with a stale pass, want 503", got)
	}

	m.lastPass.Set(1)
	res, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	for _, name := range []string{"node_controller_last_pass_timestamp_seconds", "node_controller_pass_errors_total", "go_goroutines"} {
		if !strings.Contains(string(body), name) {
			t.Errorf("/metrics lacks %s", name)
		}
	}
}
