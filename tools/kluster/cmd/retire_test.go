package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

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
