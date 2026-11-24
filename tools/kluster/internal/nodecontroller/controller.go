// Package nodecontroller is the cluster side of node-controller (ADR 0016,
// Amendment 2026-11-24): it reads the Nodes and VolumeAttachments from an
// informer cache, asks internal/deadnode what to do, and carries that out
// through the API, with an Event, a log line and a metric for each step.
//
// It runs as one replica and holds no state that matters: the clock of a
// Worker is its Ready condition's lastTransitionTime and the start of its
// drain is the taint's timeAdded, both on the Node. Every step is safe to
// repeat, so a restart, or two copies for a moment during a rollout, does no
// harm.
package nodecontroller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	storagelisters "k8s.io/client-go/listers/storage/v1"
	"k8s.io/client-go/tools/record"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/deadnode"
)

// The Event reasons, as kubectl get events shows them.
const (
	EventTainted  = "OutOfServiceTaint"
	EventRemoved  = "NodeRemoved"
	EventUntaint  = "BackInService"
	EventHeld     = "RemovalHeld"
	controlPlane  = "node-role.kubernetes.io/control-plane"
	heartbeatEach = 10 * time.Minute
)

// Controller is one node-controller.
type Controller struct {
	client      kubernetes.Interface
	nodes       corelisters.NodeLister
	attachments storagelisters.VolumeAttachmentLister
	events      record.EventRecorder
	metrics     *Metrics
	cfg         deadnode.Config
	now         func() time.Time
	log         *slog.Logger

	// held is the hold of each Node at the last pass, so a hold gives one
	// Event when it starts or changes, not one every pass.
	held map[string]deadnode.Reason
	// lastPass is the time of the last pass that ended without an error,
	// in Unix nanoseconds; 0 before the first.
	lastPass      atomic.Int64
	lastHeartbeat time.Time
}

// New makes a Controller. The listers must come from informers that have
// started and synced.
func New(client kubernetes.Interface, nodes corelisters.NodeLister, attachments storagelisters.VolumeAttachmentLister,
	events record.EventRecorder, m *Metrics, cfg deadnode.Config, log *slog.Logger) *Controller {
	return &Controller{
		client: client, nodes: nodes, attachments: attachments, events: events, metrics: m,
		cfg: cfg, now: time.Now, log: log, held: map[string]deadnode.Reason{},
	}
}

// Run makes a pass at once and then every interval until ctx ends. A failed
// pass is logged and counted; the next one tries again.
func (c *Controller) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := c.Pass(ctx); err != nil && ctx.Err() == nil {
			c.metrics.passErrors.Inc()
			c.log.Error("pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// LastPass is when the last pass without an error ended; zero before the
// first.
func (c *Controller) LastPass() time.Time {
	if ns := c.lastPass.Load(); ns != 0 {
		return time.Unix(0, ns)
	}
	return time.Time{}
}

// Pass reads the cluster, decides, and acts once.
func (c *Controller) Pass(ctx context.Context) error {
	nodes, err := c.nodes.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("list Nodes: %w", err)
	}
	vas, err := c.attachments.List(labels.Everything())
	if err != nil {
		return fmt.Errorf("list VolumeAttachments: %w", err)
	}
	perNode := map[string]int{}
	for _, va := range vas {
		perNode[va.Spec.NodeName]++
	}

	now := c.now()
	in := make([]deadnode.Node, 0, len(nodes))
	byName := make(map[string]*corev1.Node, len(nodes))
	for _, n := range nodes {
		d := Read(n, perNode[n.Name])
		if !d.Ready && !d.Tainted.IsZero() {
			// Only a tainted Node waits on its pods, so only its pods are
			// listed, straight from the API.
			pods, err := c.client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + n.Name})
			if err != nil {
				return fmt.Errorf("list the pods of %s: %w", n.Name, err)
			}
			d.Pods = len(pods.Items)
		}
		in = append(in, d)
		byName[n.Name] = n
	}

	actions := deadnode.Decide(in, now, c.cfg)
	c.metrics.held.Reset()
	holds := map[string]deadnode.Reason{}
	var errs []error
	for _, a := range actions {
		if a.Kind == deadnode.Hold {
			c.hold(a, byName[a.Node])
			holds[a.Node] = a.Reason
			continue
		}
		if err := c.act(ctx, a, byName[a.Node], now); err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", a.Kind, a.Node, err))
		}
	}
	c.held = holds
	c.heartbeat(now, in, len(actions))
	if err := errors.Join(errs...); err != nil {
		return err
	}
	c.lastPass.Store(now.UnixNano())
	c.metrics.lastPass.Set(float64(now.Unix()))
	return nil
}

// hold records a held Node: a metric every pass, an Event and a log line
// when the hold starts or changes. A pause is the operator's own choice, so
// its Event is Normal.
func (c *Controller) hold(a deadnode.Action, n *corev1.Node) {
	c.metrics.held.WithLabelValues(a.Node, string(a.Reason)).Set(1)
	if c.held[a.Node] == a.Reason {
		return
	}
	kind := corev1.EventTypeWarning
	if a.Reason == deadnode.Paused {
		kind = corev1.EventTypeNormal
	}
	c.events.Event(n, kind, EventHeld, a.Detail)
	c.log.Info("held", "node", a.Node, "reason", a.Reason, "detail", a.Detail)
}

// act carries out a Taint, Untaint or Delete on a fresh copy of the Node. It
// does nothing when the Node it decided on is gone or was replaced by a new
// one of the same name, or when a Taint or Delete finds it Ready again: the
// next pass decides afresh.
func (c *Controller) act(ctx context.Context, a deadnode.Action, seen *corev1.Node, now time.Time) error {
	nodes := c.client.CoreV1().Nodes()
	n, err := nodes.Get(ctx, a.Node, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if n.UID != seen.UID {
		c.log.Info("skipped: the Node was replaced since the pass read it", "node", a.Node, "action", a.Kind)
		return nil
	}
	if a.Kind != deadnode.Untaint && isReady(n) {
		c.log.Info("skipped: the Node turned Ready since the pass read it", "node", a.Node, "action", a.Kind)
		return nil
	}

	switch a.Kind {
	case deadnode.Taint:
		if hasTaint(n, deadnode.OutOfServiceTaint) {
			return nil
		}
		at := metav1.NewTime(now)
		n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{
			Key: deadnode.OutOfServiceTaint, Value: deadnode.OutOfServiceValue,
			Effect: corev1.TaintEffectNoExecute, TimeAdded: &at,
		})
		if _, err := nodes.Update(ctx, n, metav1.UpdateOptions{}); err != nil {
			return err
		}
		c.events.Event(n, corev1.EventTypeWarning, EventTainted, a.Detail)
	case deadnode.Untaint:
		if !hasTaint(n, deadnode.OutOfServiceTaint) {
			return nil
		}
		n.Spec.Taints = slices.DeleteFunc(n.Spec.Taints, func(t corev1.Taint) bool { return t.Key == deadnode.OutOfServiceTaint })
		if _, err := nodes.Update(ctx, n, metav1.UpdateOptions{}); err != nil {
			return err
		}
		c.events.Event(n, corev1.EventTypeNormal, EventUntaint, a.Detail)
	case deadnode.Delete:
		// The Event goes first: it names the Node object, which the delete
		// takes away.
		c.events.Event(n, corev1.EventTypeWarning, EventRemoved, a.Detail+". Bring the machine back with: kluster node add "+a.Node)
		uid := n.UID
		err := nodes.Delete(ctx, a.Node, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
		if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
			return nil
		}
		if err != nil {
			return err
		}
		c.metrics.removed.WithLabelValues(a.Node, string(a.Reason)).Set(float64(now.Unix()))
	default:
		return fmt.Errorf("unknown action %q", a.Kind)
	}
	c.metrics.actions.WithLabelValues(string(a.Kind), a.Node, string(a.Reason)).Inc()
	c.log.Info(string(a.Kind), "node", a.Node, "reason", a.Reason, "detail", a.Detail)
	return nil
}

// heartbeat logs what the pass saw, every heartbeatEach, so the log shows the
// controller at work while it has nothing to do.
func (c *Controller) heartbeat(now time.Time, in []deadnode.Node, actions int) {
	if now.Sub(c.lastHeartbeat) < heartbeatEach {
		return
	}
	c.lastHeartbeat = now
	workers, ready := 0, 0
	for _, n := range in {
		if n.ControlPlane {
			continue
		}
		workers++
		if n.Ready {
			ready++
		}
	}
	c.log.Info("pass", "workers", workers, "ready", ready, "actions", actions)
}

// Read turns a Node and the count of its VolumeAttachments into what the
// rules read. The Control Plane is the Node with kubeadm's control-plane
// label or taint: either one is enough to keep it out.
func Read(n *corev1.Node, volumes int) deadnode.Node {
	d := deadnode.Node{
		Name:        n.Name,
		Since:       n.CreationTimestamp.Time,
		ProviderOff: hasTaint(n, deadnode.ShutdownTaint),
		Pause:       n.Annotations[deadnode.PauseAnnotation],
		Volumes:     volumes,
	}
	_, label := n.Labels[controlPlane]
	d.ControlPlane = label || hasTaint(n, controlPlane)
	for _, cond := range n.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			d.Ready = cond.Status == corev1.ConditionTrue
			if !cond.LastTransitionTime.IsZero() {
				d.Since = cond.LastTransitionTime.Time
			}
		}
	}
	for _, t := range n.Spec.Taints {
		if t.Key == deadnode.OutOfServiceTaint {
			// A taint added by hand may carry no time: its drain counts from
			// the Node's last change of readiness instead, which is earlier.
			d.Tainted = d.Since
			if t.TimeAdded != nil {
				d.Tainted = t.TimeAdded.Time
			}
		}
	}
	return d
}

func isReady(n *corev1.Node) bool {
	for _, cond := range n.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

func hasTaint(n *corev1.Node, key string) bool {
	return slices.ContainsFunc(n.Spec.Taints, func(t corev1.Taint) bool { return t.Key == key })
}
