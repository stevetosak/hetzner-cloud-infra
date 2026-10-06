package kube

import (
	"context"
	"os"
	"testing"
)

const nodesJSON = `{"items":[
 {"metadata":{"name":"k8s-cp","labels":{"node-role.kubernetes.io/control-plane":""}},
  "status":{"conditions":[{"type":"MemoryPressure","status":"False"},{"type":"Ready","status":"True"}],
   "addresses":[{"type":"InternalIP","address":"10.0.1.5"},{"type":"Hostname","address":"k8s-cp"}],
   "nodeInfo":{"kubeletVersion":"v1.37.1"}}},
 {"metadata":{"name":"k8swk1","labels":{"role":"worker"}},
  "status":{"conditions":[{"type":"Ready","status":"Unknown"}],
   "addresses":[{"type":"InternalIP","address":"10.0.2.6"},{"type":"ExternalIP","address":"203.0.113.6"}],
   "nodeInfo":{"kubeletVersion":"v1.37.1"}}}
]}`

func TestParseNodes(t *testing.T) {
	nodes, err := ParseNodes([]byte(nodesJSON))
	if err != nil {
		t.Fatal(err)
	}
	want := []Node{
		{Name: "k8s-cp", Ready: true, InternalIP: "10.0.1.5", Kubelet: "v1.37.1", ControlPlane: true},
		{Name: "k8swk1", Ready: false, InternalIP: "10.0.2.6", Kubelet: "v1.37.1"},
	}
	if len(nodes) != len(want) {
		t.Fatalf("got %+v", nodes)
	}
	for i := range want {
		if nodes[i] != want[i] {
			t.Errorf("node %d = %+v, want %+v", i, nodes[i], want[i])
		}
	}
}

type recorder struct{ cmd string }

func (r *recorder) Run(_ context.Context, cmd string) (string, error)            { r.cmd = cmd; return "", nil }
func (r *recorder) WriteFile(context.Context, string, string, os.FileMode) error { return nil }
func (r *recorder) ReadFile(context.Context, string) (string, error)             { return "", nil }

func TestRunUsesTheAdminKubeconfigAndQuotes(t *testing.T) {
	r := &recorder{}
	if _, err := (Kubectl{Exec: r}).Run(context.Background(), "drain", "k8swk1", "--selector=a b"); err != nil {
		t.Fatal(err)
	}
	if want := "KUBECONFIG=/etc/kubernetes/admin.conf kubectl 'drain' 'k8swk1' '--selector=a b'"; r.cmd != want {
		t.Errorf("ran %q, want %q", r.cmd, want)
	}
}

func TestParseNodesReadsProviderIDAndTheCloudTaint(t *testing.T) {
	nodes, err := ParseNodes([]byte(`{"items":[{"metadata":{"name":"k8swk4"},"spec":{"providerID":"hcloud://42",
		"taints":[{"key":"node.cloudprovider.kubernetes.io/uninitialized","effect":"NoSchedule"}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if nodes[0].ProviderID != "hcloud://42" || !nodes[0].Uninitialized {
		t.Fatalf("got %+v", nodes[0])
	}
}
