package stages

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

const (
	getRS     = kc + "'get' 'replicasets' '--all-namespaces'"
	getCRD    = kc + "'get' 'crd' 'clusters.postgresql.cnpg.io'"
	getDBs    = kc + "'get' 'clusters.postgresql.cnpg.io' '--all-namespaces'"
	getDB     = kc + "'-n' 'pg-cluster' 'get' 'clusters.postgresql.cnpg.io' 'pg'"
	getPGPod  = kc + "'-n' 'pg-cluster' 'get' 'pod' "
	patchDB   = kc + "'-n' 'pg-cluster' 'patch' 'clusters.postgresql.cnpg.io' 'pg'"
	webStatus = kc + "'-n' 'apps' 'rollout' 'status' 'deployment/web'"
)

// clusterHost is a Control Plane whose CloudNativePG Cluster follows a
// switchover: a status patch makes the target the current primary. A
// command that starts with fail returns an error.
type clusterHost struct {
	scriptHost
	fail      string
	instances int
	primary   string
}

func (c *clusterHost) Run(ctx context.Context, cmd string) (string, error) {
	if c.fail != "" && strings.HasPrefix(cmd, c.fail) {
		c.ran = append(c.ran, cmd)
		return "", errors.New("timed out waiting for the condition")
	}
	if strings.HasPrefix(cmd, patchDB) {
		_, after, _ := strings.Cut(cmd, `"targetPrimary":"`)
		c.primary, _, _ = strings.Cut(after, `"`)
	}
	c.out[getDB] = c.database()
	c.out[getDBs] = `{"items":[` + c.database() + `]}`
	return c.scriptHost.Run(ctx, cmd)
}

func (c *clusterHost) database() string {
	return fmt.Sprintf(`{"metadata":{"namespace":"pg-cluster","name":"pg"},"spec":{"instances":%d},`+
		`"status":{"readyInstances":%d,"phase":%q,"currentPrimary":%q,"targetPrimary":%q}}`,
		c.instances, c.instances, kube.CNPGHealthyPhase, c.primary, c.primary)
}

type testPod struct {
	ns, name, node, ip, ownerKind, ownerName, phase string
	hostNetwork                                     bool
}

func podsJSON(pods ...testPod) string {
	var items []string
	for _, p := range pods {
		owners := "[]"
		if p.ownerKind != "" {
			owners = fmt.Sprintf(`[{"kind":%q,"name":%q,"controller":true}]`, p.ownerKind, p.ownerName)
		}
		phase := p.phase
		if phase == "" {
			phase = "Running"
		}
		items = append(items, fmt.Sprintf(`{"metadata":{"namespace":%q,"name":%q,"ownerReferences":%s},`+
			`"spec":{"nodeName":%q,"hostNetwork":%t,"containers":[]},"status":{"phase":%q,"podIP":%q}}`,
			p.ns, p.name, owners, p.node, p.hostNetwork, phase, p.ip))
	}
	return `{"items":[` + strings.Join(items, ",") + `]}`
}

// replicaSets: web-abc belongs to Deployment web, lonely to nobody.
const replicaSets = `{"items":[
 {"metadata":{"namespace":"apps","name":"web-abc","ownerReferences":[{"kind":"Deployment","name":"web","controller":true}]}},
 {"metadata":{"namespace":"apps","name":"lonely"}}]}`

func readyNodes(names ...string) string {
	var items []string
	for _, n := range names {
		items = append(items, `{"metadata":{"name":"`+n+`"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}`)
	}
	return `{"items":[` + strings.Join(items, ",") + `]}`
}

// staleCluster is a cluster after flannel-mtu on every node: a mix of pods
// at 1400 and 1350, of every kind of owner.
func staleCluster() (*clusterHost, *scriptHost, map[string]stage.Exec) {
	pods := podsJSON(
		testPod{ns: "apps", name: "web-abc-1", node: "k8swk2", ip: "10.244.1.5", ownerKind: "ReplicaSet", ownerName: "web-abc"},
		testPod{ns: "apps", name: "web-abc-2", node: "k8swk2", ip: "10.244.1.6", ownerKind: "ReplicaSet", ownerName: "web-abc"},
		testPod{ns: "apps", name: "lonely-1", node: "k8swk2", ip: "10.244.1.7", ownerKind: "ReplicaSet", ownerName: "lonely"},
		testPod{ns: "apps", name: "bare", node: "k8swk2", ip: "10.244.1.8"},
		testPod{ns: "apps", name: "job-1", node: "k8swk2", ip: "10.244.1.9", ownerKind: "Job", ownerName: "job"},
		testPod{ns: "apps", name: "new", node: "k8swk2", ip: "10.244.1.10", ownerKind: "ReplicaSet", ownerName: "web-abc"},
		testPod{ns: "apps", name: "waiting", node: "", ownerKind: "ReplicaSet", ownerName: "web-abc", phase: "Pending"},
		testPod{ns: "kube-flannel", name: "flannel-x", node: "k8swk2", ip: "10.0.0.3", ownerKind: "DaemonSet", ownerName: "flannel", hostNetwork: true},
		testPod{ns: "monitoring", name: "prometheus-0", node: "k8s-cp", ip: "10.244.0.4", ownerKind: "StatefulSet", ownerName: "prometheus"},
		testPod{ns: "pg-cluster", name: "pg-1", node: "k8s-cp", ip: "10.244.0.5", ownerKind: "Cluster", ownerName: "pg"},
		testPod{ns: "pg-cluster", name: "pg-2", node: "k8swk2", ip: "10.244.1.11", ownerKind: "Cluster", ownerName: "pg"},
	)
	cp := &clusterHost{instances: 2, primary: "pg-2", scriptHost: scriptHost{out: map[string]string{
		getPods: pods, getRS: replicaSets, getNodes: readyNodes("k8s-cp", "k8swk2"),
		getCRD:   "customresourcedefinition.apiextensions.k8s.io/clusters.postgresql.cnpg.io",
		getPGPod: "True", podMTUScript[:10]: "10.244.0.4 1400\n10.244.0.5 1400\n",
	}}}
	wk := &scriptHost{out: map[string]string{podMTUScript[:10]: "10.244.1.5 1400\n10.244.1.6 1350\n10.244.1.7 1400\n" +
		"10.244.1.8 1400\n10.244.1.9 1400\n10.244.1.11 1400\n"}}
	return cp, wk, map[string]stage.Exec{"k8s-cp": cp, "k8swk2": wk}
}

func podMTURestart(nodes map[string]stage.Exec) PodMTURestart {
	return PodMTURestart{Nodes: nodes, PodMTU: 1350, Wait: time.Second, Every: time.Millisecond}
}

// writes are the commands that change the cluster.
func writes(ran []string) []string {
	var w []string
	for _, c := range ran {
		if strings.Contains(c, "'rollout'") || strings.Contains(c, "'delete'") || strings.Contains(c, "'patch'") {
			w = append(w, strings.TrimPrefix(c, kc))
		}
	}
	return w
}

const leftAlone = "apps/lonely-1 (MTU 1400, a ReplicaSet without a Deployment), apps/bare (MTU 1400, a bare pod), " +
	"apps/job-1 (MTU 1400, owned by a Job), apps/new (not measured yet)"

func TestPodMTURestartProbeNamesStaleWorkloadsByOwner(t *testing.T) {
	cp, _, nodes := staleCluster()
	st, err := podMTURestart(nodes).Probe(context.Background(), &stage.Host{Exec: cp})
	want := "not yet: pods not at MTU 1350 in apps/Deployment/web, monitoring/StatefulSet/prometheus, pg-cluster/Cluster/pg; left alone: " + leftAlone
	if err != nil || st.Done || st.Detail != want {
		t.Fatalf("got %+v %v\nwant %q", st, err, want)
	}
}

func TestPodMTURestartRefusesAPodOnANodeWithoutALogin(t *testing.T) {
	cp, _, nodes := staleCluster()
	delete(nodes, "k8swk2")
	_, err := podMTURestart(nodes).Probe(context.Background(), &stage.Host{Exec: cp})
	if err == nil || !strings.Contains(err.Error(), "runs on k8swk2, which kluster has no login to") {
		t.Fatalf("got %v", err)
	}
}

// Rollouts go first, in name order; the database goes last: its replica,
// then a switchover to it, then the old primary.
func TestPodMTURestartActsInOrderDatabaseLast(t *testing.T) {
	cp, _, nodes := staleCluster()
	if err := podMTURestart(nodes).Act(context.Background(), &stage.Host{Exec: cp}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"'-n' 'apps' 'rollout' 'restart' 'deployment/web'",
		"'-n' 'apps' 'rollout' 'status' 'deployment/web' '--timeout=1s'",
		"'-n' 'monitoring' 'rollout' 'restart' 'statefulset/prometheus'",
		"'-n' 'monitoring' 'rollout' 'status' 'statefulset/prometheus' '--timeout=1s'",
		"'-n' 'pg-cluster' 'delete' 'pod' 'pg-1' '--timeout=1s'",
		`'-n' 'pg-cluster' 'patch' 'clusters.postgresql.cnpg.io' 'pg' '--type' 'merge' '--subresource' 'status' '-p' '{"status":{"targetPrimary":"pg-1"}}'`,
		"'-n' 'pg-cluster' 'delete' 'pod' 'pg-2' '--timeout=1s'",
	}
	if got := writes(cp.ran); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("wrote\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if cp.primary != "pg-1" {
		t.Fatalf("primary %s", cp.primary)
	}
}

// A Cluster of one instance has nothing to switch over to: its pod restarts.
func TestPodMTURestartOneInstanceDatabase(t *testing.T) {
	cp, wk, nodes := staleCluster()
	cp.instances, cp.primary = 1, "pg-2"
	cp.out[getPods] = podsJSON(testPod{ns: "pg-cluster", name: "pg-2", node: "k8swk2", ip: "10.244.1.11", ownerKind: "Cluster", ownerName: "pg"})
	if err := podMTURestart(nodes).Act(context.Background(), &stage.Host{Exec: cp}); err != nil {
		t.Fatal(err)
	}
	if got := writes(cp.ran); len(got) != 1 || got[0] != "'-n' 'pg-cluster' 'delete' 'pod' 'pg-2' '--timeout=1s'" {
		t.Fatalf("wrote %q", got)
	}
	if len(writes(wk.ran)) > 0 {
		t.Fatalf("wrote on the worker %q", wk.ran)
	}
}

// The first restart that does not finish stops the run: no rollout and no
// database restart follows a rollout that does not fit.
func TestPodMTURestartStopsAtTheFirstFailedRollout(t *testing.T) {
	cp, _, nodes := staleCluster()
	cp.fail = webStatus
	err := podMTURestart(nodes).Act(context.Background(), &stage.Host{Exec: cp})
	if err == nil || err.Error() != "apps/Deployment/web: timed out waiting for the condition; the restarts after it did not run" {
		t.Fatalf("got %v", err)
	}
	got := writes(cp.ran)
	if len(got) != 2 || !strings.HasPrefix(kc+got[1], webStatus) {
		t.Fatalf("wrote %q", got)
	}
}

// A switchover that does not finish stops before the old primary restarts.
func TestPodMTURestartStopsWhenTheSwitchoverDoesNotFinish(t *testing.T) {
	cp, _, nodes := staleCluster()
	cp.out[getPods] = podsJSON(
		testPod{ns: "pg-cluster", name: "pg-1", node: "k8s-cp", ip: "10.244.0.5", ownerKind: "Cluster", ownerName: "pg"},
		testPod{ns: "pg-cluster", name: "pg-2", node: "k8swk2", ip: "10.244.1.11", ownerKind: "Cluster", ownerName: "pg"})
	stuck := &stuckSwitchover{clusterHost: cp}
	nodes["k8s-cp"] = stuck
	r := podMTURestart(nodes)
	r.Wait = 50 * time.Millisecond
	err := r.Act(context.Background(), &stage.Host{Exec: stuck})
	if err == nil || !strings.Contains(err.Error(), "the switchover to pg-1 did not finish in 50ms") {
		t.Fatalf("got %v", err)
	}
	for _, w := range writes(cp.ran) {
		if strings.Contains(w, "'pg-2'") {
			t.Fatalf("the old primary restarted: %q", w)
		}
	}
}

// stuckSwitchover takes the status patch but never changes the primary.
type stuckSwitchover struct{ *clusterHost }

func (s *stuckSwitchover) Run(ctx context.Context, cmd string) (string, error) {
	keep := s.primary
	out, err := s.clusterHost.Run(ctx, cmd)
	s.primary = keep
	s.out[getDB] = s.database()
	return out, err
}

func TestPodMTURestartPreview(t *testing.T) {
	cp, _, nodes := staleCluster()
	r := podMTURestart(nodes)
	p, err := r.Preview(context.Background(), &stage.Host{Exec: cp})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"kubectl -n apps rollout restart deployment/web && kubectl -n apps rollout status deployment/web --timeout=1s\n",
		"# pg-cluster/Cluster/pg: delete each replica pod, switch over to a replica, delete the old primary pod\n",
		"# left alone: apps/bare (MTU 1400, a bare pod)\n",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("preview lacks %q:\n%s", want, p)
		}
	}
	if strings.Index(p, "prometheus") > strings.Index(p, "pg-cluster/Cluster/pg") {
		t.Errorf("the database is not last:\n%s", p)
	}
	if p, _ := r.Preview(context.Background(), &stage.Host{}); !strings.HasPrefix(p, "# restart, one at a time") {
		t.Errorf("plan mode without a login: %q", p)
	}
}

func TestPodMTUProof(t *testing.T) {
	cp, wk, nodes := staleCluster()
	proof := PodMTUProof{Nodes: nodes, PodMTU: 1350, Wait: time.Second, Every: time.Millisecond}
	st, err := proof.Probe(context.Background(), &stage.Host{Exec: cp})
	if err != nil || st.Done || st.Detail != "not yet: apps/waiting scheduled, apps/Deployment/web at MTU 1350, "+
		"monitoring/StatefulSet/prometheus at MTU 1350, pg-cluster/Cluster/pg at MTU 1350" {
		t.Fatalf("got %+v %v", st, err)
	}
	cp.out[getPods] = podsJSON(
		testPod{ns: "apps", name: "web-abc-1", node: "k8swk2", ip: "10.244.1.5", ownerKind: "ReplicaSet", ownerName: "web-abc"},
		testPod{ns: "apps", name: "bare", node: "k8swk2", ip: "10.244.1.8"},
		testPod{ns: "pg-cluster", name: "pg-1", node: "k8s-cp", ip: "10.244.0.5", ownerKind: "Cluster", ownerName: "pg"})
	cp.out[podMTUScript[:10]] = "10.244.0.5 1350\n"
	wk.out[podMTUScript[:10]] = "10.244.1.5 1350\n10.244.1.8 1400\n"
	st, err = proof.Probe(context.Background(), &stage.Host{Exec: cp})
	if err != nil || !st.Done || st.Detail != "2 nodes Ready, no pod Pending, 1 databases healthy, every workload at MTU 1350; "+
		"left alone: apps/bare (MTU 1400, a bare pod)" {
		t.Fatalf("got %+v %v", st, err)
	}
	cp.out[getDBs] = `{"items":[{"metadata":{"namespace":"pg-cluster","name":"pg"},"spec":{"instances":2},"status":{"readyInstances":1,"phase":"Switchover in progress"}}]}`
	sick := &scriptHost{out: cp.out}
	nodes["k8s-cp"] = sick
	proof.Nodes = nodes
	st, _ = proof.Probe(context.Background(), &stage.Host{Exec: sick})
	if st.Done || st.Detail != "not yet: pg-cluster/pg healthy (Switchover in progress)" {
		t.Fatalf("an unhealthy database passed: %+v", st)
	}
}

// The restart and proof Probes and the Preview only read.
func TestPodMTURestartProbesWriteNothing(t *testing.T) {
	cp, wk, nodes := staleCluster()
	h := &stage.Host{Exec: cp}
	r := podMTURestart(nodes)
	proof := PodMTUProof{Nodes: nodes, PodMTU: 1350, Wait: time.Second, Every: time.Millisecond}
	for _, s := range []stage.Stage{r, proof} {
		if _, err := s.Probe(context.Background(), h); err != nil {
			t.Fatalf("%s: %v", s.Name(), err)
		}
		if s.Runbook() != podMTURunbook {
			t.Errorf("%s names %s", s.Name(), s.Runbook())
		}
	}
	if _, err := r.Preview(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if w := append(writes(cp.ran), writes(wk.ran)...); len(w) > 0 {
		t.Fatalf("a probe wrote %q", w)
	}
	for _, c := range append(cp.ran, wk.ran...) {
		if !strings.HasPrefix(c, kc+"'get'") && !strings.HasPrefix(c, kc+"'-n' 'pg-cluster' 'get'") && c != podMTUScript {
			t.Errorf("a probe ran %q", c)
		}
	}
}
