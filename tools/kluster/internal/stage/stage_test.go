package stage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

// fake is a Stage whose done state lives in the test.
type fake struct {
	name     string
	done     *bool
	acts     *int32
	actWorks bool
	actErr   error
}

func (f *fake) Name() string    { return f.name }
func (f *fake) Runbook() string { return "docs/runbook/x.md#" + f.name }
func (f *fake) Probe(context.Context, *Host) (Status, error) {
	return Status{Done: *f.done, Detail: "probed"}, nil
}
func (f *fake) Preview(context.Context, *Host) (string, error) { return "echo " + f.name, nil }
func (f *fake) Act(context.Context, *Host) error {
	atomic.AddInt32(f.acts, 1)
	if f.actErr != nil {
		return f.actErr
	}
	if f.actWorks {
		*f.done = true
	}
	return nil
}

func newFake(name string, done bool) *fake {
	return &fake{name: name, done: &done, acts: new(int32), actWorks: true}
}

func TestPlanModeOnlyProbesAndPreviews(t *testing.T) {
	a, b := newFake("a", true), newFake("b", false)
	var out bytes.Buffer
	if err := Run(context.Background(), []Stage{a, b}, &Host{Name: "h"}, Options{Out: &out}); err != nil {
		t.Fatal(err)
	}
	if *a.acts+*b.acts != 0 {
		t.Fatal("Plan Mode acted")
	}
	for _, want := range []string{"a: done", "b: would run", "docs/runbook/x.md#b", "echo b"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// A run that stopped after the first Stage resumes at the second.
func TestApplyResumesAtTheFirstUndoneStage(t *testing.T) {
	a, b, c := newFake("a", true), newFake("b", false), newFake("c", false)
	if err := Run(context.Background(), []Stage{a, b, c}, &Host{Name: "h"}, Options{Apply: true, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if *a.acts != 0 || *b.acts != 1 || *c.acts != 1 {
		t.Fatalf("acts a=%d b=%d c=%d", *a.acts, *b.acts, *c.acts)
	}
}

func TestApplyFailsWhenTheProbeDisagreesWithTheAct(t *testing.T) {
	a := newFake("a", false)
	a.actWorks = false
	err := Run(context.Background(), []Stage{a}, &Host{Name: "h"}, Options{Apply: true, Out: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "still says not done") {
		t.Fatalf("got %v", err)
	}
}

func TestApplyStopsAtAFailingStage(t *testing.T) {
	a, b := newFake("a", false), newFake("b", false)
	a.actErr = errors.New("boom")
	if err := Run(context.Background(), []Stage{a, b}, &Host{Name: "h"}, Options{Apply: true, Out: io.Discard}); err == nil {
		t.Fatal("no error")
	}
	if *b.acts != 0 {
		t.Fatal("ran past a failed stage")
	}
}

// One host failing does not stop the others.
func TestRunAllFinishesEveryHost(t *testing.T) {
	failing := newFake("x", false)
	failing.actErr = errors.New("boom")
	ok := newFake("x", false)
	ok.done = new(bool)
	// Each host gets its own stage list through a per-host Stage.
	perHost := &router{byHost: map[string]Stage{"bad": failing, "good": ok}}
	hosts := []*Host{{Name: "bad"}, {Name: "good"}}
	if err := RunAll(context.Background(), []Stage{perHost}, hosts, Options{Apply: true, Out: io.Discard}); err == nil {
		t.Fatal("no error from the failing host")
	}
	if *ok.acts != 1 {
		t.Fatal("the healthy host did not finish")
	}
}

type router struct{ byHost map[string]Stage }

func (r *router) Name() string    { return "routed" }
func (r *router) Runbook() string { return "docs/runbook/x.md#routed" }
func (r *router) Probe(ctx context.Context, h *Host) (Status, error) {
	return r.byHost[h.Name].Probe(ctx, h)
}
func (r *router) Preview(ctx context.Context, h *Host) (string, error) {
	return r.byHost[h.Name].Preview(ctx, h)
}
func (r *router) Act(ctx context.Context, h *Host) error { return r.byHost[h.Name].Act(ctx, h) }
