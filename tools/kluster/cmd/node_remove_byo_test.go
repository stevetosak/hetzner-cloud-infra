package cmd

import (
	"bytes"
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/meshstore"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/nodeset"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// removalLog records each step of a byo removal or rebuild as "where:step".
type removalLog struct {
	steps      []string
	fail       string // the step that fails
	nodes      []kube.Node
	machineErr error
}

func (l *removalLog) step(s string) error {
	l.steps = append(l.steps, s)
	if s == l.fail {
		return errors.New(s + " failed")
	}
	return nil
}

func (l *removalLog) runStages(_ context.Context, s []stage.Stage, h *stage.Host) error {
	for _, st := range s {
		if err := l.step(h.Name + ":" + st.Name()); err != nil {
			return err
		}
	}
	return nil
}

func (l *removalLog) machine(context.Context) (*stage.Host, func(), error) {
	if l.machineErr != nil {
		return nil, nil, l.machineErr
	}
	return &stage.Host{Name: "tswk1"}, func() { l.steps = append(l.steps, "tswk1:logout") }, nil
}

// logStore is a mesh store that writes into the log.
type logStore struct {
	*fakeMeshStore
	log *removalLog
}

func (s logStore) Remove(ctx context.Context, name string) error {
	if err := s.log.step("store:remove " + name); err != nil {
		return err
	}
	return s.fakeMeshStore.Remove(ctx, name)
}

func newTestRemoval(t *testing.T, l *removalLog, apply, unreachable bool) (*byoRemoval, *bytes.Buffer) {
	t.Helper()
	set := parseMeshSet(t, byoNodeSet)
	m, _ := set.Mesh().Machine("tswk1")
	updated, err := set.WithoutMachine("tswk1")
	if err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	return &byoRemoval{
		out: out, apply: apply, set: set, m: m, updated: updated,
		cp: &stage.Host{Name: "k8s-cp"}, cpName: "k8s-cp", unreachable: unreachable,
		runStages: l.runStages,
		retirable: func(context.Context) error { return l.step("gates") },
		nodes:     func(context.Context) ([]kube.Node, error) { return l.nodes, nil },
		machine:   l.machine,
		waitGone:  func(context.Context) error { return l.step("wait-gone") },
		store:     logStore{&fakeMeshStore{entries: map[string]meshstore.Entry{}}, l},
		writeSet: func(b []byte) (*nodeset.Set, error) {
			if err := l.step("node-set:write"); err != nil {
				return nil, err
			}
			return parseMeshSet(t, string(b)), nil
		},
		dropPin: func(addr string) error { return l.step("pin:drop " + addr) },
		converge: func(_ context.Context, back *nodeset.Set) error {
			if meshDeclares(back.Mesh(), "tswk1") {
				t.Error("the mesh converges with tswk1 still declared")
			}
			return l.step("mesh:converge")
		},
	}, out
}

var forgetSteps = []string{"store:remove tswk1", "node-set:write", "pin:drop 10.101.1.1", "mesh:converge"}

// The Node goes before the machine is reset, the machine leaves before its
// records go, and the mesh converges last, without it.
func TestByoRemovalOrder(t *testing.T) {
	l := &removalLog{}
	r, out := newTestRemoval(t, l, true, false)
	if err := r.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := slices.Concat([]string{"gates", "k8s-cp:drain", "k8s-cp:delete-node", "tswk1:byo-reset", "tswk1:byo-leave", "tswk1:logout", "wait-gone"}, forgetSteps)
	if !slices.Equal(l.steps, want) {
		t.Errorf("steps\n%v\nwant\n%v", l.steps, want)
	}
	if !strings.Contains(out.String(), "commit test.yaml: it no longer declares tswk1") {
		t.Errorf("no commit hint:\n%s", out)
	}
}

// A failed gate, or a machine kluster cannot reach, stops the run before any
// change; the second names --unreachable.
func TestByoRemovalStopsBeforeAnyChange(t *testing.T) {
	for name, c := range map[string]struct {
		l    *removalLog
		want string
	}{
		"a gate":      {&removalLog{fail: "gates"}, "gates failed"},
		"unreachable": {&removalLog{machineErr: errors.New("no route to host")}, "--unreachable takes out a dead"},
		"the drain":   {&removalLog{fail: "k8s-cp:drain"}, "k8s-cp:drain failed"},
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := newTestRemoval(t, c.l, true, false)
			err := r.run(context.Background())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
			for _, s := range c.l.steps {
				if slices.Contains(forgetSteps, s) || strings.HasPrefix(s, "tswk1:byo") || s == "k8s-cp:delete-node" {
					t.Errorf("%s ran after the stop: %v", s, c.l.steps)
				}
			}
		})
	}
}

// --unreachable deletes the Node without a drain and without the machine,
// and refuses a Node that is Ready.
func TestByoRemovalUnreachable(t *testing.T) {
	notReady := []kube.Node{{Name: "k8swk1", Ready: true}, {Name: "tswk1"}}
	l := &removalLog{nodes: notReady, machineErr: errors.New("must not dial")}
	r, _ := newTestRemoval(t, l, true, true)
	if err := r.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := slices.Concat([]string{"gates", "k8s-cp:cordon", "k8s-cp:delete-node"}, forgetSteps)
	if !slices.Equal(l.steps, want) {
		t.Errorf("steps\n%v\nwant\n%v", l.steps, want)
	}

	l = &removalLog{nodes: []kube.Node{{Name: "tswk1", Ready: true}}}
	r, _ = newTestRemoval(t, l, true, true)
	if err := r.run(context.Background()); err == nil || !strings.Contains(err.Error(), "Node tswk1 is Ready") {
		t.Fatalf("got %v", err)
	}
	if !slices.Equal(l.steps, []string{"gates"}) {
		t.Errorf("a Ready Node changed: %v", l.steps)
	}
}

// Plan Mode runs the Stages, which only print, and changes no record.
func TestByoRemovalPlanModeKeepsTheRecords(t *testing.T) {
	l := &removalLog{}
	r, out := newTestRemoval(t, l, false, false)
	if err := r.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"gates", "k8s-cp:drain", "k8s-cp:delete-node", "tswk1:byo-reset", "tswk1:byo-leave", "tswk1:logout"}
	if !slices.Equal(l.steps, want) {
		t.Errorf("steps\n%v\nwant\n%v", l.steps, want)
	}
	if !strings.Contains(out.String(), "-  tswk1: {site: ts1, provisioner: byo, meshAddress: 10.101.1.1}") {
		t.Errorf("the Node set diff is not shown:\n%s", out)
	}
}

// A mesh that fails to converge leaves the records written and names the
// command that finishes it.
func TestByoRemovalConvergeFailureNamesNodeMesh(t *testing.T) {
	l := &removalLog{fail: "mesh:converge"}
	r, _ := newTestRemoval(t, l, true, false)
	err := r.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "kluster node mesh --apply, then commit test.yaml") {
		t.Fatalf("got %v", err)
	}
	if !slices.Contains(l.steps, "node-set:write") {
		t.Errorf("the Node set was not written: %v", l.steps)
	}
}

func TestRemoveFlagsBelongToByo(t *testing.T) {
	if err := (removeFlags{unreachable: true}).check(nodeset.ProvisionerHcloud); err == nil || !strings.Contains(err.Error(), "--unreachable: not a flag of provisioner hcloud") {
		t.Fatalf("got %v", err)
	}
	for _, kind := range []string{nodeset.ProvisionerHcloud, nodeset.ProvisionerByo} {
		if err := (removeFlags{}).check(kind); err != nil {
			t.Error(err)
		}
	}
	if err := (removeFlags{unreachable: true}).check(nodeset.ProvisionerByo); err != nil {
		t.Error(err)
	}
}

// waitGone ends at the first dial that fails, and fails while every dial
// answers.
func TestWaitGone(t *testing.T) {
	ctx := context.Background()
	dials := 0
	answersTwice := func(context.Context, string, string) (net.Conn, error) {
		dials++
		if dials <= 2 {
			a, b := net.Pipe()
			_ = b.Close()
			return a, nil
		}
		return nil, errors.New("i/o timeout")
	}
	if err := waitGone(ctx, answersTwice, "10.101.1.1:22", time.Millisecond, time.Second); err != nil || dials != 3 {
		t.Fatalf("err %v after %d dials", err, dials)
	}
	always := func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		_ = b.Close()
		return a, nil
	}
	if err := waitGone(ctx, always, "10.101.1.1:22", time.Millisecond, 10*time.Millisecond); err == nil || !strings.Contains(err.Error(), "still answers") {
		t.Fatalf("got %v", err)
	}
}

func newTestRebuild(l *removalLog, apply bool) *byoRebuild {
	return &byoRebuild{
		apply:     apply,
		m:         nodeset.Machine{Name: "tswk1"},
		cp:        &stage.Host{Name: "k8s-cp"},
		preflight: func(context.Context) (string, error) { return "ca", l.step("preflight") },
		retirable: func(context.Context) error { return l.step("gates") },
		machine:   l.machine,
		runStages: l.runStages,
		planBuild: func(context.Context) error { return l.step("plan-build") },
		build: func(_ context.Context, ca string) error {
			if ca != "ca" {
				return errors.New("the build lost the CA the preflight read")
			}
			return l.step("build")
		},
	}
}

// node replace of a byo machine: every check first, the Node out, the
// machine reset, its session closed, then the build. The mesh and the site
// firewall stay.
func TestByoRebuildOrder(t *testing.T) {
	l := &removalLog{}
	if err := newTestRebuild(l, true).run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"preflight", "gates", "k8s-cp:drain", "k8s-cp:delete-node", "tswk1:byo-reset", "tswk1:logout", "build"}
	if !slices.Equal(l.steps, want) {
		t.Errorf("steps\n%v\nwant\n%v", l.steps, want)
	}

	l = &removalLog{}
	if err := newTestRebuild(l, false).run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := l.steps[len(l.steps)-1]; got != "plan-build" {
		t.Errorf("Plan Mode ends with %s, want plan-build: %v", got, l.steps)
	}
}

func TestByoRebuildStopsBeforeAnyChange(t *testing.T) {
	for name, c := range map[string]struct {
		l    *removalLog
		want string
	}{
		"the preflight": {&removalLog{fail: "preflight"}, "preflight failed"},
		"a gate":        {&removalLog{fail: "gates"}, "gates failed"},
		"unreachable":   {&removalLog{machineErr: errors.New("no route to host")}, "kluster node remove tswk1 --unreachable"},
	} {
		t.Run(name, func(t *testing.T) {
			err := newTestRebuild(c.l, true).run(context.Background())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
			if slices.ContainsFunc(c.l.steps, func(s string) bool { return strings.Contains(s, ":") || s == "build" }) {
				t.Errorf("a change ran: %v", c.l.steps)
			}
		})
	}
}

// A failed reset closes the session and builds nothing.
func TestByoRebuildStopsAfterAFailedReset(t *testing.T) {
	l := &removalLog{fail: "tswk1:byo-reset"}
	if err := newTestRebuild(l, true).run(context.Background()); err == nil {
		t.Fatal("no error")
	}
	if slices.Contains(l.steps, "build") || !slices.Contains(l.steps, "tswk1:logout") {
		t.Errorf("steps %v", l.steps)
	}
}
