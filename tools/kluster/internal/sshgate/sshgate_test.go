package sshgate

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/intent"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/tf"
)

// fakeShared returns its plans in order and records applies.
type fakeShared struct {
	plans   [][]intent.Change
	applied int
}

func (f *fakeShared) Plan(context.Context, map[string]any) (*tf.Plan, error) {
	if len(f.plans) == 0 {
		return &tf.Plan{Module: "shared"}, nil
	}
	p := &tf.Plan{Module: "shared", Changes: f.plans[0]}
	f.plans = f.plans[1:]
	return p, nil
}

func (f *fakeShared) Apply(context.Context, *tf.Plan) error { f.applied++; return nil }

// fakeAPI holds which firewalls admit port 22. clearWorks false models a
// set_rules that does not take.
type fakeAPI struct {
	open       map[string]bool
	clearWorks bool
	cleared    []string
}

func (f *fakeAPI) SSHOpen(_ context.Context, name string) (bool, error) { return f.open[name], nil }

func (f *fakeAPI) ClearSSH(_ context.Context, name string) error {
	f.cleared = append(f.cleared, name)
	if f.clearWorks {
		f.open[name] = false
	}
	return nil
}

var closeWorker = []intent.Change{{Address: "hcloud_firewall.worker", Action: intent.Update}}

// The workers runbook, step 9: the apply succeeds, the rule survives. The
// Gate must notice, clear it over the API and prove the result.
func TestCloseClearsARuleTheApplyLeft(t *testing.T) {
	shared := &fakeShared{plans: [][]intent.Change{closeWorker, nil}}
	api := &fakeAPI{open: map[string]bool{Worker.Name: true}, clearWorks: true}
	g := &Gate{Shared: shared, API: api, Mode: tf.Mode{Apply: true}, Out: io.Discard}

	if err := g.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if shared.applied != 1 || len(api.cleared) != 1 || api.cleared[0] != Worker.Name {
		t.Fatalf("applied=%d cleared=%v", shared.applied, api.cleared)
	}
}

func TestCloseFailsWhileTheRuleSurvives(t *testing.T) {
	shared := &fakeShared{plans: [][]intent.Change{closeWorker}}
	api := &fakeAPI{open: map[string]bool{Worker.Name: true}}
	g := &Gate{Shared: shared, API: api, Mode: tf.Mode{Apply: true}, Out: io.Discard}

	if err := g.Close(context.Background()); !errors.Is(err, ErrStillOpen) {
		t.Fatalf("got %v, want ErrStillOpen", err)
	}
}

func TestCloseFailsWhenThePlanIsNotCleanAfterwards(t *testing.T) {
	shared := &fakeShared{plans: [][]intent.Change{closeWorker, closeWorker}}
	api := &fakeAPI{open: map[string]bool{}}
	g := &Gate{Shared: shared, API: api, Mode: tf.Mode{Apply: true}, Out: io.Discard}

	if err := g.Close(context.Background()); err == nil {
		t.Fatal("a dirty plan after the close passed")
	}
}

// Plan Mode never writes, so it never clears; an open port still fails the
// run.
func TestClosePlanModeWritesNothing(t *testing.T) {
	shared := &fakeShared{plans: [][]intent.Change{closeWorker}}
	api := &fakeAPI{open: map[string]bool{Worker.Name: true}, clearWorks: true}
	g := &Gate{Shared: shared, API: api, Out: io.Discard}

	if err := g.Close(context.Background()); !errors.Is(err, ErrStillOpen) {
		t.Fatalf("got %v", err)
	}
	if shared.applied != 0 || len(api.cleared) != 0 {
		t.Fatal("Plan Mode wrote")
	}
}

// An open whose plan also touches the Control Plane firewall aborts.
func TestOpenRefusesAPlanThatTouchesTheOtherFirewall(t *testing.T) {
	shared := &fakeShared{plans: [][]intent.Change{{
		{Address: "hcloud_firewall.cp", Action: intent.Update},
		{Address: "hcloud_firewall.worker", Action: intent.Update},
	}}}
	api := &fakeAPI{open: map[string]bool{}}
	g := &Gate{Shared: shared, API: api, Mode: tf.Mode{Apply: true}, Out: io.Discard}

	if err := g.Open(context.Background(), Worker); !errors.Is(err, tf.ErrIntent) {
		t.Fatalf("got %v", err)
	}
	if shared.applied != 0 {
		t.Fatal("applied a mismatching plan")
	}
}

func TestOpenReadsBack(t *testing.T) {
	shared := &fakeShared{plans: [][]intent.Change{closeWorker}}
	api := &fakeAPI{open: map[string]bool{}}
	g := &Gate{Shared: shared, API: api, Mode: tf.Mode{Apply: true}, Out: io.Discard}

	if err := g.Open(context.Background(), Worker); err == nil {
		t.Fatal("an open the API does not show passed")
	}
}
