package nodelist

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

func worker(name, private, vpn string) workerset.Worker {
	return workerset.Worker{Name: name, PrivateIP: private, VpnIP: vpn}
}

func server(name, private string) *cloud.Server {
	return &cloud.Server{Name: name, Status: "running", PrivateIPs: []string{private}}
}

func node(name, ip string, ready bool) kube.Node {
	return kube.Node{Name: name, InternalIP: ip, Ready: ready, Kubelet: "v1.37.1"}
}

func byName(rows []Row) map[string]Row {
	m := map[string]Row{}
	for _, r := range rows {
		m[r.Name] = r
	}
	return m
}

func TestCompareInStep(t *testing.T) {
	rows := Compare(Input{
		Set:          []workerset.Worker{worker("k8swk1", "10.0.2.6", "10.100.0.2")},
		Servers:      []*cloud.Server{server("k8s-cp", "10.0.1.5"), server("k8swk1", "10.0.2.6")},
		Nodes:        []kube.Node{{Name: "k8s-cp", ControlPlane: true, Ready: true}, node("k8swk1", "10.0.2.6", true)},
		NodesRead:    true,
		ControlPlane: "k8s-cp",
	})
	if len(rows) != 1 || rows[0].Name != "k8swk1" || HasDrift(rows) {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestCompareMarksEachDrift(t *testing.T) {
	stopped := server("k8swk3", "10.0.2.8")
	stopped.Status = "off"
	rows := byName(Compare(Input{
		Set: []workerset.Worker{
			worker("k8swk1", "10.0.2.6", "10.100.0.2"), // no server
			worker("k8swk2", "10.0.2.7", "10.100.0.3"), // server, not joined
			worker("k8swk3", "10.0.2.8", "10.100.0.4"), // off, NotReady
			worker("k8swk4", "10.0.2.9", "10.100.0.5"), // wrong private IP
		},
		Servers:   []*cloud.Server{server("k8swk2", "10.0.2.7"), stopped, server("k8swk4", "10.0.2.10"), server("stray", "10.0.2.20")},
		Nodes:     []kube.Node{node("k8swk3", "10.0.2.8", false), node("k8swk4", "10.0.2.10", true), node("ghost", "10.0.2.30", true)},
		NodesRead: true,
	}))
	want := map[string][]string{
		"k8swk1": {"no server"},
		"k8swk2": {"not joined"},
		"k8swk3": {"server off", "NotReady"},
		"k8swk4": {"server private IP [10.0.2.10], set 10.0.2.9", "Node InternalIP 10.0.2.10, set 10.0.2.9"},
		"stray":  {"server not in the Worker set", "not joined"},
		"ghost":  {"Node without a server"},
	}
	for name, w := range want {
		if got := rows[name].Drift; !slices.Equal(got, w) {
			t.Errorf("%s: drift %q, want %q", name, got, w)
		}
	}
}

// In a rehearsal with bootstrap SSH closed the Nodes are not read: that is
// unknown, not "not joined".
func TestCompareWithoutNodesMarksNoNodeDrift(t *testing.T) {
	in := Input{
		Set:     []workerset.Worker{worker("k8swk1", "10.0.2.6", "10.100.0.2")},
		Servers: []*cloud.Server{server("k8swk1", "10.0.2.6")},
	}
	rows := Compare(in)
	if HasDrift(rows) {
		t.Fatalf("drift without Nodes: %+v", rows)
	}
	var out bytes.Buffer
	if err := Print(&out, rows, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "?") {
		t.Errorf("the Node column must show unknown:\n%s", out.String())
	}
}
