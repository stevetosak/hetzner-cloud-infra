package stages

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kubeletcfg"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

const (
	kc        = "KUBECONFIG=/etc/kubernetes/admin.conf kubectl "
	getRaw    = kc + "'get' '--raw' '/api/v1/nodes/k8swk2/proxy/configz'"
	getCM     = kc + "'-n' 'kube-system' 'get' 'configmap' 'kubelet-config'"
	patchCM   = kc + "'-n' 'kube-system' 'patch' 'configmap' 'kubelet-config'"
	capacity  = "3911564Ki"
	allocNow  = "3809164Ki" // capacity - 100Mi
	allocNext = "3153804Ki" // capacity - 160Mi - 480Mi - 100Mi
)

// wantKubelet is the live proposal (2026-11-15).
func wantKubelet() map[string]any {
	return map[string]any{
		"kubeReserved":              map[string]string{"memory": "160Mi"},
		"systemReserved":            map[string]string{"memory": "480Mi"},
		"evictionSoft":              map[string]string{"memory.available": "300Mi"},
		"evictionSoftGracePeriod":   map[string]string{"memory.available": "1m"},
		"evictionMaxPodGracePeriod": 30,
	}
}

// configz answers the node proxy's /configz; applied adds wantKubelet.
func configz(t *testing.T, applied bool) string {
	cfg := map[string]any{"cgroupDriver": "systemd", "evictionHard": map[string]string{"memory.available": "100Mi"}}
	if applied {
		for k, v := range wantKubelet() {
			cfg[k] = v
		}
	}
	b, err := json.Marshal(map[string]any{"kubeletconfig": cfg})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func workerNode(ready bool, allocatable string) string {
	r := "False"
	if ready {
		r = "True"
	}
	return `{"items":[{"metadata":{"name":"k8swk2"},"status":{"conditions":[{"type":"Ready","status":"` + r +
		`"}],"capacity":{"memory":"` + capacity + `"},"allocatable":{"memory":"` + allocatable + `"}}}]}`
}

// podsRequesting puts one running pod per request on the node, and one
// finished pod that requests 4Gi and so must not count.
func podsRequesting(reqs ...string) string {
	var items []string
	for i, r := range reqs {
		items = append(items, `{"metadata":{"namespace":"ns","name":"p`+string(rune('a'+i))+`"},"spec":{"containers":[{"resources":{"requests":{"memory":"`+r+`"}}}]},"status":{"phase":"Running"}}`)
	}
	items = append(items, `{"metadata":{"namespace":"ns","name":"done"},"spec":{"containers":[{"resources":{"requests":{"memory":"4Gi"}}}]},"status":{"phase":"Succeeded"}}`)
	return `{"items":[` + strings.Join(items, ",") + `]}`
}

func TestKubeletRoomRefusesANodeWithoutRoom(t *testing.T) {
	r := KubeletRoom{Node: "k8swk2", Want: wantKubelet()}
	for _, c := range []struct {
		name    string
		applied bool
		pods    string
		done    bool
		detail  string
	}{
		{"fits", false, podsRequesting("2000Mi", "1000Mi"), true, "requests 3000Mi fit the new allocatable 3079Mi"},
		{"too much", false, podsRequesting("2000Mi", "1100Mi"), false, "not yet: requests 3100Mi are above the new allocatable 3079Mi"},
		{"already running the values", true, podsRequesting("2000Mi", "1100Mi"), true, "the kubelet already runs the values"},
	} {
		sh := &scriptHost{out: map[string]string{getRaw: configz(t, c.applied), getNodes: workerNode(true, allocNow), getPods: c.pods}}
		h := &stage.Host{Exec: sh}
		st, err := r.Probe(context.Background(), h)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if st.Done != c.done || st.Detail != c.detail {
			t.Errorf("%s: got %+v, want done %v %q", c.name, st, c.done, c.detail)
		}
		err = r.Act(context.Background(), h)
		if c.done && err != nil {
			t.Errorf("%s: Act refused a node with room: %v", c.name, err)
		}
		if !c.done && (err == nil || !strings.Contains(err.Error(), "move pods off it first (largest: ns/pa 2000Mi, ns/pb 1100Mi)")) {
			t.Errorf("%s: Act gave %v", c.name, err)
		}
	}
}

func cmJSON(t *testing.T, doc string) string {
	b, err := json.Marshal(map[string]any{"data": map[string]string{"kubelet": doc}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestKubeletConfigMapPatchesOnlyTheWantedFields(t *testing.T) {
	raw, err := os.ReadFile("../kubeletcfg/testdata/kubeadm.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c := KubeletConfigMap{Want: wantKubelet()}
	sh := &scriptHost{out: map[string]string{getCM: cmJSON(t, string(raw))}}
	h := &stage.Host{Exec: sh}
	if st, err := c.Probe(context.Background(), h); err != nil || st.Done {
		t.Fatalf("a ConfigMap without the values is done: %+v %v", st, err)
	}
	p, err := c.Preview(context.Background(), h)
	if err != nil || !strings.Contains(p, "+kubeReserved:\n+  memory: 160Mi") {
		t.Fatalf("preview %q %v", p, err)
	}
	if err := c.Act(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	last := sh.ran[len(sh.ran)-1]
	if !strings.HasPrefix(last, patchCM) {
		t.Fatalf("ran %q", last)
	}
	// The patch is the last quoted argument: {"data":{"kubelet":"…"}}.
	arg := last[strings.LastIndex(last, " '")+2 : len(last)-1]
	var patch struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.ReplaceAll(arg, `'\''`, `'`)), &patch); err != nil {
		t.Fatalf("patch %q: %v", arg, err)
	}
	before, _ := kubeletcfg.Parse(string(raw))
	after, err := kubeletcfg.Parse(patch.Data["kubelet"])
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := kubeletcfg.Differ(after, wantKubelet()); len(d) > 0 {
		t.Fatalf("the patch lacks %v", d)
	}
	if after["containerRuntimeEndpoint"] != before["containerRuntimeEndpoint"] || after["cgroupDriver"] != "systemd" {
		t.Fatal("the patch changed a field outside the values")
	}
	sh.out[getCM] = cmJSON(t, patch.Data["kubelet"])
	if st, _ := c.Probe(context.Background(), h); !st.Done {
		t.Fatalf("the patched ConfigMap is not done: %+v", st)
	}
}

func TestKubeletConfigFileBacksUpThenRunsKubeadm(t *testing.T) {
	f := KubeletConfigFile{Want: wantKubelet(), Now: func() time.Time { return time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC) }}
	fh := &fileHost{files: map[string]string{kubeletConfigFile: "cgroupDriver: systemd\n"}}
	h := &stage.Host{Exec: fh}
	if st, _ := f.Probe(context.Background(), h); st.Done {
		t.Fatal("a file without the values is done")
	}
	if err := f.Act(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	script := fh.written[scriptDir+"/kubelet-config.sh"]
	want := "cp -p /var/lib/kubelet/config.yaml /var/lib/kubelet/config.yaml.bak-kluster-20261116T090000Z\nkubeadm upgrade node phase kubelet-config\n"
	if script != strict+want {
		t.Fatalf("script %q", script)
	}
}

func appliedFile(t *testing.T) string {
	b, err := json.Marshal(wantKubelet())
	if err != nil {
		t.Fatal(err)
	}
	return string(b) // JSON is YAML
}

func TestKubeletRestartRefusesAnOldFile(t *testing.T) {
	cp := &scriptHost{out: map[string]string{getRaw: configz(t, true)}}
	r := KubeletRestart{Node: "k8swk2", Want: wantKubelet(), CP: cp, Wait: time.Second, Every: time.Millisecond}
	node := &fileHost{files: map[string]string{kubeletConfigFile: "cgroupDriver: systemd\n"}}
	err := r.Act(context.Background(), &stage.Host{Exec: node})
	if err == nil || !strings.Contains(err.Error(), "only on the new file") {
		t.Fatalf("got %v", err)
	}
	if len(node.ran) > 0 {
		t.Fatalf("ran %v on an old file", node.ran)
	}
	node.files[kubeletConfigFile] = appliedFile(t)
	if err := r.Act(context.Background(), &stage.Host{Exec: node}); err != nil {
		t.Fatal(err)
	}
	if len(node.ran) != 1 || node.ran[0] != "systemctl restart kubelet" {
		t.Fatalf("ran %v", node.ran)
	}
	if st, _ := r.Probe(context.Background(), nil); !st.Done {
		t.Fatalf("the running kubelet holds the values, but not done: %+v", st)
	}
}

func TestKubeletProof(t *testing.T) {
	p := KubeletProof{Node: "k8swk2", Want: wantKubelet(), Wait: 0, Every: time.Millisecond}
	rejectedPod := `{"items":[{"metadata":{"namespace":"ns","name":"big"},"status":{"phase":"Failed","reason":"OutOfmemory"}}]}`
	for _, c := range []struct {
		name          string
		applied, read bool
		alloc, pods   string
		done          bool
		detail        string
	}{
		{"proven", true, true, allocNext, podsRequesting("1Gi"), true, "Ready, allocatable 3079Mi, no rejected pod"},
		{"old allocatable", true, true, allocNow, podsRequesting("1Gi"), false, "not yet: allocatable 3079Mi is 3719Mi"},
		{"not ready", true, false, allocNext, podsRequesting("1Gi"), false, "not yet: Ready"},
		{"rejected pod", true, true, allocNext, rejectedPod, false, "not yet: no rejected pod (ns/big OutOfmemory)"},
		{"old kubelet", false, true, allocNow, podsRequesting("1Gi"), false, "not yet: the kubelet runs evictionMaxPodGracePeriod, evictionSoft, evictionSoftGracePeriod, kubeReserved, systemReserved"},
	} {
		sh := &scriptHost{out: map[string]string{getRaw: configz(t, c.applied), getNodes: workerNode(c.read, c.alloc), getPods: c.pods}}
		h := &stage.Host{Exec: sh}
		st, err := p.Probe(context.Background(), h)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if st.Done != c.done || st.Detail != c.detail {
			t.Errorf("%s: got %+v, want done %v %q", c.name, st, c.done, c.detail)
		}
		if err := p.Act(context.Background(), h); err != nil {
			t.Errorf("%s: Act: %v", c.name, err)
		}
	}
}

// Every kubelet Probe only reads.
func TestKubeletProbesWriteNothing(t *testing.T) {
	raw, _ := os.ReadFile("../kubeletcfg/testdata/kubeadm.yaml")
	sh := &fileHost{scriptHost: scriptHost{out: map[string]string{getRaw: configz(t, false), getNodes: workerNode(true, allocNow),
		getPods: podsRequesting("1Gi"), getCM: cmJSON(t, string(raw))}}, files: map[string]string{kubeletConfigFile: string(raw)}}
	h := &stage.Host{Exec: sh}
	for _, s := range []stage.Stage{KubeletRoom{Node: "k8swk2", Want: wantKubelet()}, KubeletConfigMap{Want: wantKubelet()},
		KubeletConfigFile{Want: wantKubelet()}, KubeletRestart{Node: "k8swk2", Want: wantKubelet(), CP: sh},
		KubeletProof{Node: "k8swk2", Want: wantKubelet()}} {
		if _, err := s.Probe(context.Background(), h); err != nil {
			t.Fatalf("%s: %v", s.Name(), err)
		}
		if s.Runbook() != kubeletRunbook {
			t.Errorf("%s names %s", s.Name(), s.Runbook())
		}
	}
	for _, c := range sh.ran {
		if !strings.HasPrefix(c, kc+"'get'") && !strings.HasPrefix(c, getCM) {
			t.Errorf("a probe ran %q", c)
		}
	}
	if len(sh.written) > 0 {
		t.Errorf("a probe wrote %v", sh.written)
	}
}
