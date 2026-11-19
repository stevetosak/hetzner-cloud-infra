package stages

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

const (
	getFlannelCM   = kc + "'-n' 'kube-flannel' 'get' 'configmap' 'kube-flannel-cfg'"
	patchFlannelCM = kc + "'-n' 'kube-flannel' 'patch' 'configmap' 'kube-flannel-cfg'"
	delFlannelPod  = kc + "'-n' 'kube-flannel' 'delete' 'pod' '-l' 'app=flannel' '--field-selector' 'spec.nodeName=k8swk2'"
	probeScript    = "if ! ( "
	threeNodes     = `{"items":[{"metadata":{"name":"k8s-cp"}},{"metadata":{"name":"k8swk2"}},{"metadata":{"name":"k8swk3"}}]}`
)

func netConf(mtu string) string {
	return "{\n  \"Network\": \"10.244.0.0/16\",\n  \"Backend\": {\n    \"Type\": \"vxlan\",\n    \"MTU\": " + mtu + "\n  }\n}\n"
}

func flannelCMJSON(t *testing.T, mtu string) string {
	b, err := json.Marshal(map[string]any{"data": map[string]string{"net-conf.json": netConf(mtu)}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// flannelManifest is a Flannel manifest whose ConfigMap is not the first
// document, as in core/cni/flannel.yaml.
func flannelManifest(mtu string) string {
	indented := "    " + strings.ReplaceAll(strings.TrimRight(netConf(mtu), "\n"), "\n", "\n    ")
	return "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: kube-flannel\n---\n" +
		"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: kube-flannel-cfg\n  namespace: kube-flannel\ndata:\n" +
		"  net-conf.json: |\n" + indented + "\n"
}

// The repository's Flannel manifest gives pods the MTU kluster.yaml names, so
// cp init and node pod-mtu build the same network.
func TestFlannelManifestAgreesWithKlusterYAML(t *testing.T) {
	manifest, err := os.ReadFile("../../../../core/cni/flannel.yaml")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../kluster.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Cluster struct {
			PodMTU int `yaml:"podMTU"`
		} `yaml:"cluster"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := (FlannelConfig{Manifest: string(manifest), PodMTU: cfg.Cluster.PodMTU}).wanted(); err != nil {
		t.Fatal(err)
	}
}

func TestFlannelConfigPatchesOnlyNetConf(t *testing.T) {
	f := FlannelConfig{Manifest: flannelManifest("1400"), PodMTU: 1350}
	sh := &scriptHost{out: map[string]string{getFlannelCM: flannelCMJSON(t, "1450")}}
	h := &stage.Host{Exec: sh}
	st, err := f.Probe(context.Background(), h)
	if err != nil || st.Done || st.Detail != "not yet: ConfigMap kube-flannel/kube-flannel-cfg has Backend.MTU 1450, pod MTU 1350 needs 1400" {
		t.Fatalf("got %+v %v", st, err)
	}
	p, err := f.Preview(context.Background(), h)
	if err != nil || !strings.Contains(p, "-    \"MTU\": 1450\n+    \"MTU\": 1400") {
		t.Fatalf("preview %q %v", p, err)
	}
	if err := f.Act(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	last := sh.ran[len(sh.ran)-1]
	if !strings.HasPrefix(last, patchFlannelCM) {
		t.Fatalf("ran %q", last)
	}
	arg := last[strings.LastIndex(last, " '")+2 : len(last)-1]
	var patch struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(arg), &patch); err != nil {
		t.Fatalf("patch %q: %v", arg, err)
	}
	if len(patch.Data) != 1 || patch.Data["net-conf.json"] != netConf("1400") {
		t.Fatalf("patch %v", patch.Data)
	}
	sh.out[getFlannelCM] = flannelCMJSON(t, "1400")
	if st, _ := f.Probe(context.Background(), h); !st.Done {
		t.Fatalf("the patched ConfigMap is not done: %+v", st)
	}
}

func TestFlannelConfigRefusesAManifestThatDisagrees(t *testing.T) {
	f := FlannelConfig{Manifest: flannelManifest("1450"), PodMTU: 1350}
	sh := &scriptHost{out: map[string]string{getFlannelCM: flannelCMJSON(t, "1450")}}
	err := f.Act(context.Background(), &stage.Host{Exec: sh})
	if err == nil || err.Error() != "core/cni/flannel.yaml has Backend.MTU 1450, but kluster.yaml's podMTU 1350 needs 1400" {
		t.Fatalf("got %v", err)
	}
	if _, err := (FlannelConfig{Manifest: "kind: Namespace\n", PodMTU: 1350}).wanted(); err == nil {
		t.Fatal("a manifest without the ConfigMap passed")
	}
	if len(sh.ran) > 0 {
		t.Fatalf("ran %v", sh.ran)
	}
}

func flannelMTU(cp *scriptHost) FlannelMTU {
	return FlannelMTU{Node: "k8swk2", PodMTU: 1350, CP: cp, Wait: time.Second, Every: time.Millisecond}
}

func TestFlannelMTURefusesAnOldConfigMap(t *testing.T) {
	cp := &scriptHost{out: map[string]string{getFlannelCM: flannelCMJSON(t, "1450"), getNodes: threeNodes}}
	node := &scriptHost{}
	err := flannelMTU(cp).Act(context.Background(), &stage.Host{Exec: node})
	if err == nil || !strings.Contains(err.Error(), "run flannel-config first") {
		t.Fatalf("got %v", err)
	}
	if len(node.ran) > 0 {
		t.Fatalf("ran %v on the node", node.ran)
	}
	for _, c := range cp.ran {
		if !strings.HasPrefix(c, kc+"'-n' 'kube-flannel' 'get'") {
			t.Fatalf("ran %q on the Control Plane", c)
		}
	}
}

// The device goes first, so the new pod cannot reuse it; cni0 goes last,
// once the new tunnel is up.
func TestFlannelMTURebuildsTheDeviceThenTheBridge(t *testing.T) {
	cp := &scriptHost{out: map[string]string{getFlannelCM: flannelCMJSON(t, "1400"), getNodes: threeNodes}}
	node := &scriptHost{}
	m := flannelMTU(cp)
	if err := m.Act(context.Background(), &stage.Host{Exec: node}); err != nil {
		t.Fatal(err)
	}
	if len(node.ran) != 3 || node.ran[0] != "if [ -e /sys/class/net/flannel.1 ]; then ip link delete flannel.1; fi" ||
		!strings.HasPrefix(node.ran[1], probeScript) || node.ran[2] != "if [ -e /sys/class/net/cni0 ]; then ip link set cni0 mtu 1350; fi" {
		t.Fatalf("ran on the node %q", node.ran)
	}
	for _, want := range []string{"cat /sys/class/net/flannel.1/mtu)\" = 1350", "grep -qx 'FLANNEL_MTU=1350' /run/flannel/subnet.env",
		"ip -4 route show dev flannel.1 | wc -l)\" -ge 2"} {
		if !strings.Contains(node.ran[1], want) {
			t.Errorf("the wait does not check %q", want)
		}
	}
	if strings.Contains(node.ran[1], "cni0") {
		t.Error("the wait checks cni0, which is set after it")
	}
	deleted := false
	for _, c := range cp.ran {
		deleted = deleted || strings.HasPrefix(c, delFlannelPod)
	}
	if !deleted {
		t.Fatalf("the Flannel pod was not deleted: %q", cp.ran)
	}
}

func TestFlannelMTUProbe(t *testing.T) {
	cp := &scriptHost{out: map[string]string{getNodes: threeNodes}}
	for _, c := range []struct {
		failed string
		done   bool
		detail string
	}{
		{"", true, "flannel.1, subnet.env, routes, cni0"},
		{"flannel.1\ncni0\n", false, "not yet: flannel.1, cni0"},
	} {
		node := &scriptHost{out: map[string]string{probeScript: c.failed}}
		st, err := flannelMTU(cp).Probe(context.Background(), &stage.Host{Exec: node})
		if err != nil || st.Done != c.done || st.Detail != c.detail {
			t.Errorf("got %+v %v, want %v %q", st, err, c.done, c.detail)
		}
	}
}

// Every pod MTU Probe only reads.
func TestPodMTUProbesWriteNothing(t *testing.T) {
	sh := &scriptHost{out: map[string]string{getFlannelCM: flannelCMJSON(t, "1450"), getNodes: threeNodes}}
	h := &stage.Host{Exec: sh}
	for _, s := range []stage.Stage{FlannelConfig{Manifest: flannelManifest("1400"), PodMTU: 1350}, flannelMTU(sh)} {
		if _, err := s.Probe(context.Background(), h); err != nil {
			t.Fatalf("%s: %v", s.Name(), err)
		}
		if s.Runbook() != podMTURunbook {
			t.Errorf("%s names %s", s.Name(), s.Runbook())
		}
	}
	for _, c := range sh.ran {
		if !strings.HasPrefix(c, kc+"'get'") && !strings.HasPrefix(c, getFlannelCM) && !strings.HasPrefix(c, probeScript) {
			t.Errorf("a probe ran %q", c)
		}
		if strings.Contains(c, "ip link set") || strings.Contains(c, "ip link delete") {
			t.Errorf("a probe changes a link: %q", c)
		}
	}
	if len(sh.written) > 0 {
		t.Errorf("a probe wrote %v", sh.written)
	}
}
