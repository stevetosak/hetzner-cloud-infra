//go:build k3d

// The k3d test: run it through k3d-test.sh, which makes the cluster and sets
// K3D_KUBECONFIG. The controller runs in the test with the token of its own
// ServiceAccount, bound by the RBAC of core/nodes/node-controller.yaml, so a
// right it lacks fails the test. The Nodes it acts on are made by hand and
// have no kubelet, which is what a dead machine looks like.
package nodecontroller

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/yaml"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/deadnode"
)

const manifest = "../../../../core/nodes/node-controller.yaml"

func TestK3d(t *testing.T) {
	path := os.Getenv("K3D_KUBECONFIG")
	if path == "" {
		t.Skip("K3D_KUBECONFIG is not set; run k3d-test.sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	adminCfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		t.Fatal(err)
	}
	admin := kubernetes.NewForConfigOrDie(adminCfg)

	waitFor(t, ctx, 3*time.Minute, "two Ready agents and the server", func() (bool, error) {
		nodes, err := admin.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		ready := 0
		for _, n := range nodes.Items {
			if isReady(&n) {
				ready++
			}
		}
		return ready == 3, nil
	})

	applyRBAC(t, ctx, admin)
	sa := saClient(t, ctx, admin, adminCfg)
	checkRights(t, ctx, sa)
	m := startController(t, ctx, sa)

	t.Run("a dead Worker with no volume is tainted, then deleted", func(t *testing.T) {
		createNode(t, ctx, admin, "fake-a")
		tainted := false
		waitFor(t, ctx, 4*time.Minute, "fake-a deleted", func() (bool, error) {
			n, err := admin.CoreV1().Nodes().Get(ctx, "fake-a", metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			tainted = tainted || hasTaint(n, deadnode.OutOfServiceTaint)
			return false, nil
		})
		waitEvents(t, ctx, admin, "fake-a", EventTainted, EventRemoved)
		if got := testutil.ToFloat64(m.removed.WithLabelValues("fake-a", "no-volume")); got == 0 {
			t.Error("removed_timestamp_seconds{fake-a,no-volume} is not set")
		}
		t.Logf("the taint was seen before the delete: %v (a 2 s pass can taint and delete between two polls)", tainted)
	})

	t.Run("a dead Worker with a VolumeAttachment is held", func(t *testing.T) {
		createAttachment(t, ctx, admin, "fake-b")
		createNode(t, ctx, admin, "fake-b")
		waitHeld(t, ctx, m, "fake-b", deadnode.VolumeAttached)
		waitEvents(t, ctx, admin, "fake-b", EventHeld)
		if n, err := admin.CoreV1().Nodes().Get(ctx, "fake-b", metav1.GetOptions{}); err != nil || hasTaint(n, deadnode.OutOfServiceTaint) {
			t.Fatalf("fake-b: err %v, or it was tainted while held", err)
		}
	})

	t.Run("half the Workers down: every one is held", func(t *testing.T) {
		createNode(t, ctx, admin, "fake-c")
		waitHeld(t, ctx, m, "fake-c", deadnode.MassGuard)
		waitHeld(t, ctx, m, "fake-b", deadnode.MassGuard)
		if err := admin.CoreV1().Nodes().Delete(ctx, "fake-c", metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		waitHeld(t, ctx, m, "fake-b", deadnode.VolumeAttached)
	})

	t.Run("its provider reports it off: tainted, drained, deleted", func(t *testing.T) {
		n, err := admin.CoreV1().Nodes().Get(ctx, "fake-b", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{Key: deadnode.ShutdownTaint, Effect: corev1.TaintEffectNoSchedule})
		if _, err := admin.CoreV1().Nodes().Update(ctx, n, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		// The VolumeAttachment stays (no attacher runs here), so the delete
		// comes after the drain wait.
		waitFor(t, ctx, 2*time.Minute, "fake-b deleted", func() (bool, error) {
			_, err := admin.CoreV1().Nodes().Get(ctx, "fake-b", metav1.GetOptions{})
			return apierrors.IsNotFound(err), nil
		})
		if got := testutil.ToFloat64(m.removed.WithLabelValues("fake-b", "provider-off")); got == 0 {
			t.Error("removed_timestamp_seconds{fake-b,provider-off} is not set")
		}
	})

	t.Run("the server and the agents were never touched", func(t *testing.T) {
		nodes, err := admin.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes.Items) != 3 {
			t.Errorf("%d Nodes left, want the 3 of the cluster", len(nodes.Items))
		}
		for _, n := range nodes.Items {
			if hasTaint(&n, deadnode.OutOfServiceTaint) {
				t.Errorf("%s carries the out-of-service taint", n.Name)
			}
		}
	})
}

// applyRBAC makes the namespace and applies the ServiceAccount and the RBAC
// objects of the manifest; the Deployment is not needed here.
func applyRBAC(t *testing.T, ctx context.Context, c kubernetes.Interface) {
	t.Helper()
	if _, err := c.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "nodes"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := utilyaml.NewYAMLReader(bufio.NewReader(f))
	kinds := map[string]bool{}
	for {
		doc, err := r.Read()
		if err != nil {
			break
		}
		var tm metav1.TypeMeta
		if err := yaml.Unmarshal(doc, &tm); err != nil {
			t.Fatal(err)
		}
		switch tm.Kind {
		case "ServiceAccount":
			var o corev1.ServiceAccount
			must(t, yaml.Unmarshal(doc, &o))
			_, err = c.CoreV1().ServiceAccounts(o.Namespace).Create(ctx, &o, metav1.CreateOptions{})
		case "ClusterRole":
			var o rbacv1.ClusterRole
			must(t, yaml.Unmarshal(doc, &o))
			_, err = c.RbacV1().ClusterRoles().Create(ctx, &o, metav1.CreateOptions{})
		case "ClusterRoleBinding":
			var o rbacv1.ClusterRoleBinding
			must(t, yaml.Unmarshal(doc, &o))
			_, err = c.RbacV1().ClusterRoleBindings().Create(ctx, &o, metav1.CreateOptions{})
		default:
			continue
		}
		if err != nil {
			t.Fatalf("apply %s: %v", tm.Kind, err)
		}
		kinds[tm.Kind] = true
	}
	if len(kinds) != 3 {
		t.Fatalf("applied %v from %s, want a ServiceAccount, a ClusterRole and a ClusterRoleBinding", kinds, manifest)
	}
}

// saClient is a client that holds only the ServiceAccount's token.
func saClient(t *testing.T, ctx context.Context, admin kubernetes.Interface, adminCfg *rest.Config) kubernetes.Interface {
	t.Helper()
	exp := int64(3600)
	tok, err := admin.CoreV1().ServiceAccounts("nodes").CreateToken(ctx, "node-controller",
		&authnv1.TokenRequest{Spec: authnv1.TokenRequestSpec{ExpirationSeconds: &exp}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := rest.AnonymousClientConfig(adminCfg)
	cfg.BearerToken = tok.Status.Token
	return kubernetes.NewForConfigOrDie(cfg)
}

// checkRights proves the token is the ServiceAccount's and holds no more
// than the manifest gives it.
func checkRights(t *testing.T, ctx context.Context, c kubernetes.Interface) {
	t.Helper()
	for _, tc := range []struct {
		attr authzv1.ResourceAttributes
		want bool
	}{
		{authzv1.ResourceAttributes{Verb: "delete", Resource: "nodes"}, true},
		{authzv1.ResourceAttributes{Verb: "update", Resource: "nodes"}, true},
		{authzv1.ResourceAttributes{Verb: "create", Resource: "nodes"}, false},
		{authzv1.ResourceAttributes{Verb: "delete", Resource: "pods", Namespace: "kube-system"}, false},
		{authzv1.ResourceAttributes{Verb: "get", Resource: "secrets", Namespace: "kube-system"}, false},
		{authzv1.ResourceAttributes{Verb: "delete", Group: "storage.k8s.io", Resource: "volumeattachments"}, false},
	} {
		r, err := c.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx,
			&authzv1.SelfSubjectAccessReview{Spec: authzv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &tc.attr}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if r.Status.Allowed != tc.want {
			t.Errorf("can %s %s in %q: %v, want %v", tc.attr.Verb, tc.attr.Resource, tc.attr.Namespace, r.Status.Allowed, tc.want)
		}
	}
}

// startController runs the controller as main.go does, with short times.
func startController(t *testing.T, ctx context.Context, c kubernetes.Interface) *Metrics {
	t.Helper()
	factory := informers.NewSharedInformerFactory(c, 0)
	nodes := factory.Core().V1().Nodes()
	vas := factory.Storage().V1().VolumeAttachments()
	ni, vi := nodes.Informer(), vas.Informer()
	b := record.NewBroadcaster(record.WithContext(ctx))
	b.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: c.CoreV1().Events("")})
	t.Cleanup(b.Shutdown)
	m := NewMetrics()
	ctl := New(c, nodes.Lister(), vas.Lister(), b.NewRecorder(scheme.Scheme, corev1.EventSource{Component: "node-controller"}),
		m, deadnode.Config{After: 20 * time.Second, DrainWait: 30 * time.Second}, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), ni.HasSynced, vi.HasSynced) {
		t.Fatal("caches did not sync")
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { ctl.Run(runCtx, 2*time.Second); close(done) }()
	t.Cleanup(func() { stop(); <-done })
	return m
}

func createNode(t *testing.T, ctx context.Context, c kubernetes.Interface, name string) {
	t.Helper()
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"node.kubernetes.io/exclude-from-external-load-balancers": "true"}},
		Spec:       corev1.NodeSpec{ProviderID: "kluster://test/" + name},
	}
	if _, err := c.CoreV1().Nodes().Create(ctx, n, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func createAttachment(t *testing.T, ctx context.Context, c kubernetes.Interface, node string) {
	t.Helper()
	pv := "pv-" + node
	va := &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{Name: "va-" + node},
		Spec: storagev1.VolumeAttachmentSpec{
			Attacher: "test.csi.example.com", NodeName: node,
			Source: storagev1.VolumeAttachmentSource{PersistentVolumeName: &pv},
		},
	}
	if _, err := c.StorageV1().VolumeAttachments().Create(ctx, va, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func waitHeld(t *testing.T, ctx context.Context, m *Metrics, node string, why deadnode.Reason) {
	t.Helper()
	waitFor(t, ctx, 4*time.Minute, fmt.Sprintf("%s held (%s)", node, why), func() (bool, error) {
		return testutil.ToFloat64(m.held.WithLabelValues(node, string(why))) == 1, nil
	})
}

func waitEvents(t *testing.T, ctx context.Context, c kubernetes.Interface, node string, reasons ...string) {
	t.Helper()
	waitFor(t, ctx, time.Minute, fmt.Sprintf("Events %v on %s", reasons, node), func() (bool, error) {
		evs, err := c.CoreV1().Events("default").List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.name=" + node})
		if err != nil {
			return false, err
		}
		seen := map[string]bool{}
		for _, e := range evs.Items {
			if e.Source.Component == "node-controller" {
				seen[e.Reason] = true
			}
		}
		for _, r := range reasons {
			if !seen[r] {
				return false, nil
			}
		}
		return true, nil
	})
}

func waitFor(t *testing.T, ctx context.Context, timeout time.Duration, what string, ok func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	start := time.Now()
	for {
		done, err := ok()
		if err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		if done {
			t.Logf("%s after %s", what, time.Since(start).Round(time.Second))
			return
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			t.Fatalf("no %s within %s", what, timeout)
		}
		time.Sleep(time.Second)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// testWriter sends the controller's log to the test's.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
