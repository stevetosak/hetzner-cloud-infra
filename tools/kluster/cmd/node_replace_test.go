package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

// The database gate (memory-decisions 2026-10-06): every instance of every
// CNPG Cluster ready; no Cluster at all passes and says so.
func TestDatabaseGate(t *testing.T) {
	msg, err := databaseGate(nil)
	if err != nil || !strings.Contains(msg, "no CNPG Cluster") {
		t.Fatalf("none: %q, %v", msg, err)
	}
	healthy := kube.Database{Namespace: "pg-cluster", Name: "tosak-pg-cluster", Instances: 3, Ready: 3}
	if _, err := databaseGate([]kube.Database{healthy}); err != nil {
		t.Fatalf("healthy: %v", err)
	}
	degraded := kube.Database{Namespace: "pg-drill", Name: "drill", Instances: 1, Ready: 0, Phase: "Setting up primary"}
	_, err = databaseGate([]kube.Database{healthy, degraded})
	if err == nil || !strings.Contains(err.Error(), "pg-drill/drill 0/1 ready (Setting up primary)") {
		t.Fatalf("degraded: %v", err)
	}
}

func TestNotLastReadyWorker(t *testing.T) {
	cp := kube.Node{Name: "k8s-cp", Ready: true, ControlPlane: true}
	for name, c := range map[string]struct {
		nodes []kube.Node
		ok    bool
	}{
		"another Ready Worker":           {[]kube.Node{cp, {Name: "k8swk1", Ready: true}, {Name: "k8swk2", Ready: true}}, true},
		"the only Ready Worker":          {[]kube.Node{cp, {Name: "k8swk1", Ready: true}, {Name: "k8swk2"}}, false},
		"its Node is not Ready":          {[]kube.Node{cp, {Name: "k8swk1"}}, true},
		"its Node is gone":               {[]kube.Node{cp, {Name: "k8swk2", Ready: true}}, true},
		"no Node at all (after cp init)": {[]kube.Node{cp}, true},
	} {
		if err := notLastReadyWorker(c.nodes, "k8swk1"); (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok %v", name, err, c.ok)
		}
	}
}

func TestResetStoppedSaysWhatIsDoneAndLeft(t *testing.T) {
	ws := []workerset.Worker{{Name: "k8swk1"}, {Name: "k8swk2"}, {Name: "k8swk3"}}
	cause := errors.New("boom")
	for i, want := range []string{
		"reset stopped at k8swk1 (replaced: none; not touched: k8swk2, k8swk3): boom",
		"reset stopped at k8swk2 (replaced: k8swk1; not touched: k8swk3): boom",
		"reset stopped at k8swk3 (replaced: k8swk1, k8swk2; not touched: none): boom",
	} {
		err := resetStopped(ws, i, cause)
		if err.Error() != want || !errors.Is(err, cause) {
			t.Errorf("%d: %v", i, err)
		}
	}
}
