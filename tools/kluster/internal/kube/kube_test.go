package kube

import (
	"context"
	"os"
	"strings"
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

func TestParsePodsMarksWhatADrainLeaves(t *testing.T) {
	pods, err := ParsePods([]byte(`{"items":[
 {"metadata":{"namespace":"kube-flannel","name":"kube-flannel-ds-x","ownerReferences":[{"kind":"DaemonSet"}]},"status":{"phase":"Running"}},
 {"metadata":{"namespace":"kube-system","name":"static","annotations":{"kubernetes.io/config.mirror":"abc"}},"status":{"phase":"Running"}},
 {"metadata":{"namespace":"pg-drill","name":"job-x","ownerReferences":[{"kind":"Job"}]},"status":{"phase":"Succeeded"}},
 {"metadata":{"namespace":"doma","name":"web-x","ownerReferences":[{"kind":"ReplicaSet"}]},"status":{"phase":"Running"}}
]}`))
	if err != nil {
		t.Fatal(err)
	}
	var remain []string
	for _, p := range pods {
		if p.Remains() {
			remain = append(remain, p.Namespace+"/"+p.Name)
		}
	}
	if len(pods) != 4 || len(remain) != 1 || remain[0] != "doma/web-x" {
		t.Fatalf("remain %v of %+v", remain, pods)
	}
}

// scripted answers each command whose text contains a key.
type scripted map[string]string

func (s scripted) Run(_ context.Context, cmd string) (string, error) {
	for k, v := range s {
		if strings.Contains(cmd, k) {
			return v, nil
		}
	}
	return "", nil
}
func (scripted) WriteFile(context.Context, string, string, os.FileMode) error { return nil }
func (scripted) ReadFile(context.Context, string) (string, error)             { return "", nil }

// A cluster without the CNPG CRD has no databases, and that is no error.
func TestDatabasesWithoutTheCRDIsNone(t *testing.T) {
	dbs, err := Kubectl{Exec: scripted{}}.Databases(context.Background())
	if err != nil || dbs != nil {
		t.Fatalf("got %v, %v", dbs, err)
	}
}

func TestDatabasesReadsInstancesAndReady(t *testing.T) {
	s := scripted{
		"'get' 'crd'": "customresourcedefinition.apiextensions.k8s.io/clusters.postgresql.cnpg.io\n",
		"'--all-namespaces'": `{"items":[
 {"metadata":{"namespace":"pg-cluster","name":"tosak-pg-cluster"},"spec":{"instances":3},
  "status":{"readyInstances":2,"phase":"Waiting for the instances to become active"}},
 {"metadata":{"namespace":"pg-drill","name":"drill"},"spec":{"instances":1},"status":{"readyInstances":1}}]}`,
	}
	dbs, err := Kubectl{Exec: s}.Databases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(dbs) != 2 || dbs[0].Healthy() || !dbs[1].Healthy() || dbs[0].Ready != 2 || dbs[0].Instances != 3 {
		t.Fatalf("got %+v", dbs)
	}
	if (Database{}).Healthy() {
		t.Error("a Cluster that asks for no instances is not healthy")
	}
}
