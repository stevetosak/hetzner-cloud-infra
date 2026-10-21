package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/hostkey"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/intent"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/tf"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

// fakeCP answers kubectl reads on the Control Plane with fixed JSON and
// records every command; a command it does not know fails.
type fakeCP struct {
	crd   string // the CNPG CRD read: "" means no CNPG
	dbs   string
	nodes string
	ran   []string
}

func (f *fakeCP) Run(_ context.Context, cmd string) (string, error) {
	f.ran = append(f.ran, cmd)
	switch {
	case strings.Contains(cmd, "'get' 'crd'"):
		return f.crd, nil
	case strings.Contains(cmd, "'get' 'clusters.postgresql.cnpg.io'"):
		return f.dbs, nil
	case strings.Contains(cmd, "'get' 'nodes'"):
		return f.nodes, nil
	}
	return "", errors.New("unexpected command " + cmd)
}

func (f *fakeCP) WriteFile(context.Context, string, string, os.FileMode) error { return nil }
func (f *fakeCP) ReadFile(context.Context, string) (string, error)             { return "", nil }

func (f *fakeCP) ranAny(sub string) bool {
	for _, c := range f.ran {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

const twoReadyWorkers = `{"items":[
 {"metadata":{"name":"k8swk1"},"status":{"conditions":[{"type":"Ready","status":"True"}]}},
 {"metadata":{"name":"k8swk2"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`

const oneReadyWorker = `{"items":[
 {"metadata":{"name":"k8swk1"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`

const degradedCNPG = `{"items":[{"metadata":{"namespace":"pg-cluster","name":"tosak-pg-cluster"},
 "spec":{"instances":3},"status":{"readyInstances":2,"phase":"Failing over"}}]}`

// node remove and node replace share retire: a CNPG instance that is not
// ready, or a Worker that is the only Ready one, stops it before the drain.
func TestRetireRunsTheChecksBeforeTheDrain(t *testing.T) {
	w := workerset.Worker{Name: "k8swk1", VpnIP: "10.100.0.4"}
	for name, c := range map[string]struct {
		f    *fakeCP
		want string
	}{
		"a CNPG instance not ready": {&fakeCP{crd: "customresourcedefinition/clusters.postgresql.cnpg.io", dbs: degradedCNPG, nodes: twoReadyWorkers}, "database gate"},
		"the only Ready Worker":     {&fakeCP{nodes: oneReadyWorker}, "only Ready Worker"},
	} {
		t.Run(name, func(t *testing.T) {
			a := &app{cfg: nodeAddConfig(), mode: tf.Mode{Apply: true}, out: io.Discard}
			err := a.retire(context.Background(), &stage.Host{Name: "k8s-cp", Exec: c.f}, w)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
			if c.f.ranAny("'drain'") || c.f.ranAny("'delete'") {
				t.Fatalf("a retire Stage ran: %v", c.f.ran)
			}
		})
	}
}

// fakeReplacer plans the same changes whatever it is asked, and counts
// applies.
type fakeReplacer struct {
	changes []intent.Change
	applied int
}

func (f *fakeReplacer) Plan(context.Context, map[string]any) (*tf.Plan, error) {
	return &tf.Plan{Module: "workers", Changes: f.changes}, nil
}

func (f *fakeReplacer) PlanReplace(ctx context.Context, vars map[string]any, _ ...string) (*tf.Plan, error) {
	return f.Plan(ctx, vars)
}

func (f *fakeReplacer) Apply(context.Context, *tf.Plan) error { f.applied++; return nil }

func (f *fakeReplacer) Output(context.Context, string, any) error {
	return errors.New("no outputs")
}

// node replace checks its plan before the Worker leaves: a plan that would
// also touch another Worker is refused before any command runs on the
// Control Plane and before a pin is dropped.
func TestReplaceRefusesAMismatchedPlanBeforeTheWorkerLeaves(t *testing.T) {
	pins, err := hostkey.OpenPins(filepath.Join(t.TempDir(), "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	kp, _ := hostkey.Generate()
	w := workerset.Worker{Name: "k8swk1", PrivateIP: "10.0.2.6", VpnIP: "10.100.0.4"}
	srv := &cloud.Server{ID: "1", Name: "k8swk1", PublicIP: "203.0.113.7"}
	for _, addr := range []string{w.VpnIP, srv.PublicIP} {
		if err := pins.Set(addr, kp.Public); err != nil {
			t.Fatal(err)
		}
	}
	workers := &fakeReplacer{changes: []intent.Change{
		{Address: `hcloud_server.workers["k8swk1"]`, Action: intent.Replace},
		{Address: `hcloud_server.workers["k8swk2"]`, Action: intent.Replace},
	}}
	cp := &fakeCP{nodes: twoReadyWorkers}
	a := &app{cfg: nodeAddConfig(), pins: pins, mode: tf.Mode{Apply: true}, out: io.Discard}

	err = a.replaceWorker(context.Background(), workers, w, srv, &cloud.Server{Name: "k8s-cp"}, &stage.Host{Name: "k8s-cp", Exec: cp})
	if !errors.Is(err, tf.ErrIntent) {
		t.Fatalf("got %v, want ErrIntent", err)
	}
	if len(cp.ran) > 0 || workers.applied > 0 {
		t.Fatalf("ran on the Control Plane %v, applied %d", cp.ran, workers.applied)
	}
	for _, addr := range []string{w.VpnIP, srv.PublicIP} {
		if ok, _ := pins.Has(addr); !ok {
			t.Fatalf("the pin at %s was dropped", addr)
		}
	}
}

// In Plan Mode with no Control Plane reached, retire says the checks are not
// run and prints the Stages.
func TestRetirePlanModeWithoutAControlPlane(t *testing.T) {
	var out strings.Builder
	a := &app{cfg: nodeAddConfig(), out: &out}
	if err := a.retire(context.Background(), nil, workerset.Worker{Name: "k8swk1", VpnIP: "10.100.0.4"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "the database gate and the last-Worker check are not run") ||
		!strings.Contains(out.String(), "kubectl drain k8swk1") {
		t.Fatalf("output:\n%s", out.String())
	}
}
