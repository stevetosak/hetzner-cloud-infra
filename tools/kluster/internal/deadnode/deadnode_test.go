package deadnode

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

var now = time.Date(2026, 11, 24, 20, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return now.Add(-d) }

// ready is a Ready Worker; down is one not Ready since d ago.
func ready(name string) Node { return Node{Name: name, Ready: true, Since: ago(48 * time.Hour)} }
func down(name string, d time.Duration) Node {
	return Node{Name: name, Since: ago(d)}
}

// cluster is two Ready Workers and the Control Plane around the Nodes under
// test, so the mass guard holds for one Worker down.
func cluster(nodes ...Node) []Node {
	return append([]Node{
		{Name: "k8s-cp", ControlPlane: true, Ready: true, Since: ago(48 * time.Hour)},
		ready("k8swk1"), ready("k8swk2"),
	}, nodes...)
}

type kr struct {
	Kind   Kind
	Node   string
	Reason Reason
}

func kinds(as []Action) []kr {
	var out []kr
	for _, a := range as {
		out = append(out, kr{a.Kind, a.Node, a.Reason})
	}
	return out
}

func TestDecide(t *testing.T) {
	tainted := func(n Node, d time.Duration) Node { n.Tainted = ago(d); return n }
	withVolumes := func(n Node, v int) Node { n.Volumes = v; return n }
	off := func(n Node) Node { n.ProviderOff = true; return n }
	paused := func(n Node, v string) Node { n.Pause = v; return n }
	pods := func(n Node, p int) Node { n.Pods = p; return n }

	tests := []struct {
		name  string
		nodes []Node
		want  []kr
	}{
		{"every Worker Ready: nothing", cluster(ready("k8swk3")), nil},
		{"not Ready for 1 ns under 20m: not yet due", cluster(down("ts1", 20*time.Minute-time.Nanosecond)), nil},
		{"Ready condition changed in the future: not due", cluster(down("ts1", -time.Hour)), nil},
		{"not Ready for 20m with no volume: taint", cluster(down("ts1", 20*time.Minute)),
			[]kr{{Taint, "ts1", NoVolume}}},
		{"the Control Plane down for an hour: never touched", []Node{
			{Name: "k8s-cp", ControlPlane: true, Since: ago(time.Hour)},
			ready("k8swk1"), ready("k8swk2"), ready("k8swk3"),
		}, nil},
		{"a VolumeAttachment and no provider report: held", cluster(withVolumes(down("k8swk3", time.Hour), 1)),
			[]kr{{Hold, "k8swk3", VolumeAttached}}},
		{"a VolumeAttachment and the provider reports it off: taint", cluster(off(withVolumes(down("k8swk3", time.Hour), 2))),
			[]kr{{Taint, "k8swk3", ProviderReportsOff}}},
		{"the provider reports it off and no volume: provider-off wins", cluster(off(down("k8swk3", time.Hour))),
			[]kr{{Taint, "k8swk3", ProviderReportsOff}}},
		{"paused until later: held", cluster(paused(down("ts1", time.Hour), "2026-11-24T21:00:00Z")),
			[]kr{{Hold, "ts1", Paused}}},
		{"a pause with an offset is read as its instant: 21:30+02:00 ended at 19:30Z", cluster(paused(down("ts1", time.Hour), "2026-11-24T21:30:00+02:00")),
			[]kr{{Taint, "ts1", NoVolume}}},
		{"a pause with an offset is read as its instant: 22:30+02:00 ends at 20:30Z", cluster(paused(down("ts1", time.Hour), "2026-11-24T22:30:00+02:00")),
			[]kr{{Hold, "ts1", Paused}}},
		{"a pause that ended: taint", cluster(paused(down("ts1", time.Hour), "2026-11-24T19:59:59Z")),
			[]kr{{Taint, "ts1", NoVolume}}},
		{"a pause that cannot be read: held", cluster(paused(down("ts1", time.Hour), "tomorrow")),
			[]kr{{Hold, "ts1", PauseInvalid}}},
		{"a pause is not read before the Worker is due", cluster(paused(down("ts1", time.Minute), "tomorrow")), nil},
		{"one of three Workers down: acts", []Node{ready("a"), ready("b"), down("c", time.Hour)},
			[]kr{{Taint, "c", NoVolume}}},
		{"two of four Workers down: both held", []Node{ready("a"), ready("b"), down("c", time.Hour), down("d", time.Hour)},
			[]kr{{Hold, "c", MassGuard}, {Hold, "d", MassGuard}}},
		{"two of five Workers down: both act", []Node{ready("a"), ready("b"), ready("e"), down("c", time.Hour), down("d", time.Hour)},
			[]kr{{Taint, "c", NoVolume}, {Taint, "d", NoVolume}}},
		{"a Worker not yet due still counts as not Ready", []Node{ready("a"), down("b", time.Minute), down("c", time.Hour)},
			[]kr{{Hold, "c", MassGuard}}},
		{"the Control Plane is not counted in the mass guard", []Node{
			{Name: "k8s-cp", ControlPlane: true, Ready: true}, ready("a"), down("b", time.Hour),
		}, []kr{{Hold, "b", MassGuard}}},
		{"a paused Worker counts as not Ready", []Node{ready("a"), ready("b"),
			paused(down("c", time.Hour), "2026-11-25T00:00:00Z"), down("d", time.Hour)},
			[]kr{{Hold, "c", Paused}, {Hold, "d", MassGuard}}},
		{"tainted, pods still there, drain running: wait", cluster(pods(tainted(down("ts1", 22*time.Minute), time.Minute), 3)), nil},
		{"tainted, pods and volumes gone: delete", cluster(tainted(down("ts1", 22*time.Minute), time.Minute)),
			[]kr{{Delete, "ts1", NoVolume}}},
		{"tainted, pods remain after the drain wait: delete anyway", cluster(pods(tainted(down("ts1", 26*time.Minute), 5*time.Minute), 2)),
			[]kr{{Delete, "ts1", NoVolume}}},
		{"tainted as off, volumes still detaching: wait", cluster(off(withVolumes(tainted(down("k8swk3", time.Hour), time.Minute), 1))), nil},
		{"tainted as off, the volume is detached: delete", cluster(off(tainted(down("k8swk3", time.Hour), 2*time.Minute))),
			[]kr{{Delete, "k8swk3", ProviderReportsOff}}},
		{"tainted, then the mass guard trips: held, not deleted", []Node{ready("a"),
			tainted(down("b", time.Hour), 10*time.Minute), down("c", time.Hour)},
			[]kr{{Hold, "b", MassGuard}, {Hold, "c", MassGuard}}},
		{"tainted, then paused: held, not deleted", cluster(paused(tainted(down("ts1", time.Hour), 10*time.Minute), "2026-11-25T00:00:00Z")),
			[]kr{{Hold, "ts1", Paused}}},
		{"tainted and Ready again: untaint", cluster(tainted(ready("ts1"), time.Minute)),
			[]kr{{Untaint, "ts1", ReadyAgain}}},
		{"actions come in the order of the names", []Node{ready("a"), ready("b"), ready("c"),
			down("z", time.Hour), withVolumes(down("m", time.Hour), 1)},
			[]kr{{Hold, "m", VolumeAttached}, {Taint, "z", NoVolume}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := kinds(Decide(tt.nodes, now, Defaults))
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Decide (-want +got):\n%s", diff)
			}
		})
	}
}

// The detail is what the operator reads in the log, the Event and the alert:
// it names the reason and, after a pause, when the pause ended.
func TestDecideDetail(t *testing.T) {
	n := down("ts1", 25*time.Minute)
	n.Pause = "2026-11-24T19:30:00Z"
	got := Decide(cluster(n), now, Defaults)
	if len(got) != 1 {
		t.Fatalf("got %d actions, want 1: %v", len(got), got)
	}
	for _, want := range []string{"ts1", "25m0s", "no-volume", "after its pause ended at 2026-11-24T19:30:00Z", "out-of-service"} {
		if !strings.Contains(got[0].Detail, want) {
			t.Errorf("detail %q lacks %q", got[0].Detail, want)
		}
	}

	m := []Node{ready("a"), down("b", time.Hour), down("c", time.Hour)}
	if d := Decide(m, now, Defaults)[0].Detail; !strings.Contains(d, "only 1 of 3 Workers are Ready") {
		t.Errorf("mass-guard detail %q does not count the Workers", d)
	}
}

// Decide does not reorder or change the caller's slice.
func TestDecideKeepsInput(t *testing.T) {
	in := []Node{down("z", time.Hour), ready("a"), ready("b")}
	keep := append([]Node(nil), in...)
	Decide(in, now, Defaults)
	if diff := cmp.Diff(keep, in); diff != "" {
		t.Errorf("input changed (-before +after):\n%s", diff)
	}
}

func TestParsePause(t *testing.T) {
	if got, err := ParsePause(""); err != nil || !got.IsZero() {
		t.Errorf(`ParsePause("") = %v, %v; want zero, nil`, got, err)
	}
	if got, err := ParsePause("2026-11-25T18:00:00Z"); err != nil || !got.Equal(time.Date(2026, 11, 25, 18, 0, 0, 0, time.UTC)) {
		t.Errorf("ParsePause = %v, %v", got, err)
	}
	if _, err := ParsePause("2026-11-25 18:00"); err == nil || !strings.Contains(err.Error(), PauseAnnotation) {
		t.Errorf("ParsePause of a bad value: err = %v, want one that names the annotation", err)
	}
}
