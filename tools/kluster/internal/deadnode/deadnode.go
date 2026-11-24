// Package deadnode holds the rules of the dead-node remover (ADR 0016,
// Amendment 2026-11-24) as one pure function: what the Nodes look like and the
// time go in, the actions come out. It knows no Kubernetes client, so its
// tests prove every rule without a cluster; internal/nodecontroller reads the
// cluster into Nodes and carries the actions out.
//
// The rules, in the order Decide applies them to a Worker that is not Ready:
//
//   - The Control Plane is never touched and never counted.
//   - A Worker becomes due 20 minutes after its Ready condition last changed.
//   - A pause annotation that has not ended holds it; one that cannot be read
//     holds it too, so a typo is seen instead of ignored.
//   - Unless more than half of the Workers are Ready, nothing is done: many
//     Workers lost at once points at the Control Plane's side.
//   - A Worker with a VolumeAttachment is acted on only when its provider
//     reports it off (the CCM's shutdown taint); otherwise it is held.
//   - Otherwise it gets the out-of-service taint, and once its pods and
//     VolumeAttachments are gone, or DrainWait has passed, it is deleted.
//
// A Worker that is Ready again while it carries the out-of-service taint gets
// the taint back off.
package deadnode

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// The keys the rules read and write on a Node.
const (
	// OutOfServiceTaint makes Kubernetes force-delete the Node's pods and
	// detach its volumes (non-graceful node shutdown). Effect NoExecute.
	OutOfServiceTaint = "node.kubernetes.io/out-of-service"
	// OutOfServiceValue is the value Kubernetes' documentation gives it.
	OutOfServiceValue = "nodeshutdown"
	// ShutdownTaint is set by a cloud controller manager on a NotReady Node
	// whose server its provider reports off (k8s.io/cloud-provider).
	ShutdownTaint = "node.cloudprovider.kubernetes.io/shutdown"
	// PauseAnnotation holds an RFC 3339 time until which the remover leaves
	// the Node alone, for planned work.
	PauseAnnotation = "tosak.net/dead-node-remover.pause-until"
)

// Config holds the two durations of the rules.
type Config struct {
	// After is how long a Worker stays not Ready before it is due.
	After time.Duration
	// DrainWait is how long a tainted Worker's pods and volumes get to go
	// before it is deleted anyway.
	DrainWait time.Duration
}

// Defaults are the values ADR 0016 sets.
var Defaults = Config{After: 20 * time.Minute, DrainWait: 5 * time.Minute}

// Node is what the rules read of one Node.
type Node struct {
	Name         string
	ControlPlane bool
	// Ready is true when the Ready condition is True; False and Unknown are
	// both not Ready.
	Ready bool
	// Since is when the Ready condition last changed, or when the Node was
	// created if it never reported one.
	Since time.Time
	// ProviderOff is true when the Node carries ShutdownTaint.
	ProviderOff bool
	// Tainted is when the out-of-service taint was added; zero when the Node
	// does not carry it.
	Tainted time.Time
	// Pause is the PauseAnnotation's value, "" when there is none.
	Pause string
	// Volumes counts the VolumeAttachments that name the Node.
	Volumes int
	// Pods counts the pods still bound to the Node. It is read only for a
	// tainted Node and is 0 otherwise.
	Pods int
}

// Kind is what an Action does.
type Kind string

const (
	Taint   Kind = "taint"
	Untaint Kind = "untaint"
	Delete  Kind = "delete"
	Hold    Kind = "hold"
)

// Reason says why an Action was allowed or, for Hold, what holds it.
type Reason string

const (
	// NoVolume: the Worker has no VolumeAttachment, so nothing can be
	// written twice.
	NoVolume Reason = "no-volume"
	// ProviderReportsOff: the provider reports the server off.
	ProviderReportsOff Reason = "provider-off"
	// ReadyAgain: the Worker turned Ready before it was deleted.
	ReadyAgain Reason = "ready-again"

	VolumeAttached Reason = "volume-attached"
	MassGuard      Reason = "mass-guard"
	Paused         Reason = "paused"
	PauseInvalid   Reason = "pause-invalid"
)

// Action is one step for one Node. Detail is a sentence for the log and the
// Event.
type Action struct {
	Kind   Kind
	Node   string
	Reason Reason
	Detail string
}

// Decide returns the actions for the Nodes at the time now, in the order of
// the Nodes' names. A Worker that is not yet due, or whose drain is still
// running, gets no action.
func Decide(nodes []Node, now time.Time, c Config) []Action {
	sorted := slices.Clone(nodes)
	slices.SortFunc(sorted, func(a, b Node) int { return cmp.Compare(a.Name, b.Name) })
	workers, ready := 0, 0
	for _, n := range sorted {
		if n.ControlPlane {
			continue
		}
		workers++
		if n.Ready {
			ready++
		}
	}
	quorum := ready*2 > workers

	var out []Action
	for _, n := range sorted {
		if a, ok := decideOne(n, now, c, quorum, ready, workers); ok {
			out = append(out, a)
		}
	}
	return out
}

func decideOne(n Node, now time.Time, c Config, quorum bool, ready, workers int) (Action, bool) {
	act := func(k Kind, r Reason, detail string, args ...any) (Action, bool) {
		return Action{Kind: k, Node: n.Name, Reason: r, Detail: fmt.Sprintf(detail, args...)}, true
	}
	if n.ControlPlane {
		return Action{}, false
	}
	if n.Ready {
		if !n.Tainted.IsZero() {
			return act(Untaint, ReadyAgain, "%s is Ready again before its delete; the out-of-service taint comes off", n.Name)
		}
		return Action{}, false
	}
	down := now.Sub(n.Since)
	if down < c.After {
		return Action{}, false
	}
	until, err := ParsePause(n.Pause)
	if err != nil {
		return act(Hold, PauseInvalid, "%s: %v", n.Name, err)
	}
	if until.After(now) {
		return act(Hold, Paused, "%s is paused until %s", n.Name, until.UTC().Format(time.RFC3339))
	}
	if !quorum {
		return act(Hold, MassGuard, "%s is not Ready for %s, but only %d of %d Workers are Ready; nothing is removed while half or more are down",
			n.Name, round(down), ready, workers)
	}
	why := allowance(n)
	if why == "" {
		return act(Hold, VolumeAttached, "%s is not Ready for %s and has %d VolumeAttachment(s), but its provider does not report it off",
			n.Name, round(down), n.Volumes)
	}
	pause := ""
	if !until.IsZero() {
		pause = ", after its pause ended at " + until.UTC().Format(time.RFC3339)
	}
	if n.Tainted.IsZero() {
		return act(Taint, why, "%s is not Ready for %s (%s%s); it gets the out-of-service taint", n.Name, round(down), why, pause)
	}
	draining := now.Sub(n.Tainted)
	switch {
	case n.Pods == 0 && n.Volumes == 0:
		return act(Delete, why, "%s is not Ready for %s (%s%s); its pods and volumes are gone, so its Node is deleted",
			n.Name, round(down), why, pause)
	case draining >= c.DrainWait:
		return act(Delete, why, "%s is not Ready for %s (%s%s); %d pod(s) and %d VolumeAttachment(s) remain after %s, and its Node is deleted anyway",
			n.Name, round(down), why, pause, n.Pods, n.Volumes, round(draining))
	}
	return Action{}, false
}

// allowance is why a due Worker may be removed, or "" when its volumes hold
// it: only a Worker whose provider reports it off may lose a volume.
func allowance(n Node) Reason {
	switch {
	case n.ProviderOff:
		return ProviderReportsOff
	case n.Volumes == 0:
		return NoVolume
	}
	return ""
}

// ParsePause reads a PauseAnnotation value. An empty value is no pause and
// gives the zero time.
func ParsePause(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("its %s %q is not an RFC 3339 time such as 2026-11-25T18:00:00Z, so it is held until the annotation is fixed or removed", PauseAnnotation, v)
	}
	return t, nil
}

func round(d time.Duration) time.Duration { return d.Round(time.Second) }
