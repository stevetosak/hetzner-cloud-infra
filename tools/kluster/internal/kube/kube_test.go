package kube

import (
	"context"
	"os"
	"reflect"
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
		{Name: "k8s-cp", Ready: true, InternalIP: "10.0.1.5", Kubelet: "v1.37.1", ControlPlane: true,
			Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""}},
		{Name: "k8swk1", Ready: false, InternalIP: "10.0.2.6", Kubelet: "v1.37.1",
			Labels: map[string]string{"role": "worker"}},
	}
	if len(nodes) != len(want) {
		t.Fatalf("got %+v", nodes)
	}
	for i := range want {
		if !reflect.DeepEqual(nodes[i], want[i]) {
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

func TestParsePodsReadsTheEffectiveMemoryRequest(t *testing.T) {
	pods, err := ParsePods([]byte(`{"items":[
 {"metadata":{"namespace":"a","name":"plain"},"spec":{"containers":[
   {"resources":{"requests":{"memory":"64Mi"}}},{"resources":{"requests":{"memory":"32Mi"}}}]},
  "status":{"phase":"Running"}},
 {"metadata":{"namespace":"a","name":"sidecar"},"spec":{
   "initContainers":[{"restartPolicy":"Always","resources":{"requests":{"memory":"48Mi"}}},
                     {"resources":{"requests":{"memory":"16Mi"}}}],
   "containers":[{"resources":{"requests":{"memory":"64Mi"}}}]},"status":{"phase":"Running"}},
 {"metadata":{"namespace":"a","name":"big-init"},"spec":{
   "initContainers":[{"resources":{"requests":{"memory":"512Mi"}}}],
   "containers":[{"resources":{"requests":{"memory":"64Mi"}}}],"overhead":{"memory":"10Mi"}},
  "status":{"phase":"Running"}},
 {"metadata":{"namespace":"a","name":"none"},"spec":{"containers":[{}]},
  "status":{"phase":"Failed","reason":"OutOfmemory"}}
]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"plain": 96, "sidecar": 112, "big-init": 522, "none": 0}
	for _, p := range pods {
		if got := p.MemoryRequest.Value() / (1 << 20); got != want[p.Name] {
			t.Errorf("%s: %dMi, want %dMi", p.Name, got, want[p.Name])
		}
	}
	if p := pods[3]; !p.Finished || p.Phase != "Failed" || p.Reason != "OutOfmemory" {
		t.Errorf("the refused pod: %+v", p)
	}
	if _, err := ParsePods([]byte(`{"items":[{"spec":{"containers":[{"resources":{"requests":{"memory":"lots"}}}]}}]}`)); err == nil {
		t.Error("a request that is no quantity was taken")
	}
}

func TestParseNodesReadsMemory(t *testing.T) {
	nodes, err := ParseNodes([]byte(`{"items":[{"metadata":{"name":"k8swk2"},
  "status":{"capacity":{"memory":"3911564Ki"},"allocatable":{"memory":"3809164Ki"}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if n := nodes[0]; n.CapacityMemory != "3911564Ki" || n.AllocatableMemory != "3809164Ki" {
		t.Errorf("got %+v", n)
	}
}

func TestPatchConfigMapDataSendsOneKey(t *testing.T) {
	r := &recorder{}
	if err := (Kubectl{Exec: r}).PatchConfigMapData(context.Background(), "kube-system", "kubelet-config", "kubelet", "a: 'b'\n"); err != nil {
		t.Fatal(err)
	}
	want := `KUBECONFIG=/etc/kubernetes/admin.conf kubectl '-n' 'kube-system' 'patch' 'configmap' 'kubelet-config' '--type' 'merge' '-p' '{"data":{"kubelet":"a: '\''b'\''\n"}}'`
	if r.cmd != want {
		t.Errorf("ran\n%s\nwant\n%s", r.cmd, want)
	}
}

func TestRemoveConfigMapKeySetsItNull(t *testing.T) {
	r := &recorder{}
	if err := (Kubectl{Exec: r}).RemoveConfigMapKey(context.Background(), "kube-system", "mesh-peers", "tswk1"); err != nil {
		t.Fatal(err)
	}
	want := `KUBECONFIG=/etc/kubernetes/admin.conf kubectl '-n' 'kube-system' 'patch' 'configmap' 'mesh-peers' '--type' 'merge' '-p' '{"data":{"tswk1":null}}'`
	if r.cmd != want {
		t.Errorf("ran\n%s\nwant\n%s", r.cmd, want)
	}
}

// answer is an Exec that prints one fixed output.
type answer struct {
	recorder
	out string
}

func (a *answer) Run(_ context.Context, cmd string) (string, error) {
	a.cmd = cmd
	return a.out, nil
}

func TestConfigMapReadsAllData(t *testing.T) {
	a := &answer{out: `{"data":{"a":"1","b":"2"}}`}
	data, found, err := (Kubectl{Exec: a}).ConfigMap(context.Background(), "kube-system", "mesh-peers")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual(data, map[string]string{"a": "1", "b": "2"}) {
		t.Errorf("data = %v", data)
	}
	want := "KUBECONFIG=/etc/kubernetes/admin.conf kubectl '-n' 'kube-system' 'get' 'configmap' 'mesh-peers' '-o' 'json' '--ignore-not-found'"
	if a.cmd != want {
		t.Errorf("ran\n%s\nwant\n%s", a.cmd, want)
	}
}

func TestConfigMapNotFound(t *testing.T) {
	_, found, err := (Kubectl{Exec: &answer{out: "\n"}}).ConfigMap(context.Background(), "kube-system", "mesh-peers")
	if err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestCreateConfigMap(t *testing.T) {
	r := &recorder{}
	if err := (Kubectl{Exec: r}).CreateConfigMap(context.Background(), "kube-system", "mesh-peers"); err != nil {
		t.Fatal(err)
	}
	if want := "KUBECONFIG=/etc/kubernetes/admin.conf kubectl '-n' 'kube-system' 'create' 'configmap' 'mesh-peers'"; r.cmd != want {
		t.Errorf("ran\n%s\nwant\n%s", r.cmd, want)
	}
}

func TestLabelSetsInOrderAndRemoves(t *testing.T) {
	r := &recorder{}
	set := map[string]string{"x/site": "hel1", "x/provider": "hcloud"}
	if err := (Kubectl{Exec: r}).Label(context.Background(), "k8swk1", set, []string{"x/reach", "x/load-balancer"}); err != nil {
		t.Fatal(err)
	}
	want := "KUBECONFIG=/etc/kubernetes/admin.conf kubectl 'label' 'node' 'k8swk1' '--overwrite' " +
		"'x/provider=hcloud' 'x/site=hel1' 'x/load-balancer-' 'x/reach-'"
	if r.cmd != want {
		t.Errorf("ran\n%s\nwant\n%s", r.cmd, want)
	}
}

func TestParsePodsReadsTheNetworkAndTheController(t *testing.T) {
	pods, err := ParsePods([]byte(`{"items":[
 {"metadata":{"namespace":"doma","name":"web-x","labels":{"app":"web"},
   "ownerReferences":[{"kind":"Other","name":"o"},{"kind":"ReplicaSet","name":"web-5d8f","controller":true}]},
  "spec":{"nodeName":"k8swk2"},"status":{"phase":"Running","podIP":"10.244.2.7"}},
 {"metadata":{"namespace":"kube-flannel","name":"flannel-x"},
  "spec":{"nodeName":"k8s-cp","hostNetwork":true},"status":{"phase":"Running","podIP":"10.0.1.5"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Pod{
		{Namespace: "doma", Name: "web-x", Phase: "Running", Node: "k8swk2", IP: "10.244.2.7",
			Owner: Owner{Kind: "ReplicaSet", Name: "web-5d8f"}, Labels: map[string]string{"app": "web"}},
		{Namespace: "kube-flannel", Name: "flannel-x", Phase: "Running", Node: "k8s-cp", IP: "10.0.1.5", HostNetwork: true},
	}
	for i := range pods {
		pods[i].MemoryRequest = want[i].MemoryRequest
	}
	if !reflect.DeepEqual(pods, want) {
		t.Fatalf("got %+v", pods)
	}
}

func TestReplicaSetOwners(t *testing.T) {
	s := scripted{"'get' 'replicasets'": `{"items":[
 {"metadata":{"namespace":"doma","name":"web-5d8f","ownerReferences":[{"kind":"Deployment","name":"web","controller":true}]}},
 {"metadata":{"namespace":"x","name":"bare"}}]}`}
	owners, err := Kubectl{Exec: s}.ReplicaSetOwners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Owner{"doma/web-5d8f": {Kind: "Deployment", Name: "web"}, "x/bare": {}}
	if !reflect.DeepEqual(owners, want) {
		t.Fatalf("got %+v", owners)
	}
}

func TestDatabasesReadsThePrimaries(t *testing.T) {
	dbs, err := ParseDatabases([]byte(`{"items":[{"metadata":{"namespace":"pg-cluster","name":"tosak-pg-cluster"},
 "spec":{"instances":2},"status":{"readyInstances":2,"currentPrimary":"tosak-pg-cluster-2","targetPrimary":"tosak-pg-cluster-1"}}]}`))
	if err != nil || len(dbs) != 1 || dbs[0].CurrentPrimary != "tosak-pg-cluster-2" || dbs[0].TargetPrimary != "tosak-pg-cluster-1" {
		t.Fatalf("got %+v %v", dbs, err)
	}
}
