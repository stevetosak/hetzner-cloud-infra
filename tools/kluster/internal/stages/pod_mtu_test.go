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

// loggedHost records each command with its host's name in one shared log,
// so a test sees the order across the Control Plane and the node.
type loggedHost struct {
	*scriptHost
	name string
	log  *[]string
}

func (l loggedHost) Run(ctx context.Context, cmd string) (string, error) {
	*l.log = append(*l.log, l.name+": "+cmd)
	return l.scriptHost.Run(ctx, cmd)
}

// A running flanneld makes a deleted flannel.1 again at its own MTU, without
// routes: the pod goes first (the new one reads the ConfigMap), then the
// device (the new pod makes it at PodMTU), then the pod again (a fresh
// flanneld programs the peers), then cni0.
func TestFlannelMTURestartsFlannelAroundTheDevice(t *testing.T) {
	var log []string
	cp := &scriptHost{out: map[string]string{getFlannelCM: flannelCMJSON(t, "1400"), getNodes: threeNodes}}
	m := flannelMTU(cp)
	m.CP = loggedHost{cp, "cp", &log}
	if err := m.Act(context.Background(), &stage.Host{Exec: loggedHost{&scriptHost{}, "node", &log}}); err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, l := range log {
		switch {
		case strings.HasPrefix(l, "cp: "+delFlannelPod):
			steps = append(steps, "delete pod")
		case l == "node: if [ -e /sys/class/net/flannel.1 ]; then ip link delete flannel.1; fi":
			steps = append(steps, "delete flannel.1")
		case l == "node: if [ -e /sys/class/net/cni0 ]; then ip link set cni0 mtu 1350; fi":
			steps = append(steps, "cni0")
		case strings.HasPrefix(l, "node: "+probeScript):
			var held []string
			for _, c := range []string{"flannel.1/mtu)\" = 1350", "'FLANNEL_MTU=1350'", "dev flannel.1 | wc -l)\" -ge 2"} {
				if strings.Contains(l, c) {
					held = append(held, c[:9])
				}
			}
			steps = append(steps, "wait "+strings.Join(held, "+"))
			if strings.Contains(l, "cni0") {
				t.Error("a wait checks cni0, which is set after it")
			}
		}
	}
	want := []string{"delete pod", "wait 'FLANNEL_", "delete flannel.1", "wait flannel.1", "delete pod",
		"wait flannel.1+'FLANNEL_+dev flann", "cni0"}
	if strings.Join(steps, " | ") != strings.Join(want, " | ") {
		t.Fatalf("steps\n got %q\nwant %q", steps, want)
	}
}

// A wait that runs out stops Act with the failing check; nothing after it runs.
func TestFlannelMTUStopsWhenAWaitRunsOut(t *testing.T) {
	cp := &scriptHost{out: map[string]string{getFlannelCM: flannelCMJSON(t, "1400"), getNodes: threeNodes}}
	node := &scriptHost{out: map[string]string{probeScript: "subnet.env\n"}}
	m := flannelMTU(cp)
	m.Wait = 5 * time.Millisecond
	err := m.Act(context.Background(), &stage.Host{Exec: node})
	if err == nil || !strings.Contains(err.Error(), "k8swk2: waited 5ms for the new Flannel pod's FLANNEL_MTU, not yet: subnet.env") {
		t.Fatalf("got %v", err)
	}
	for _, c := range node.ran {
		if !strings.HasPrefix(c, probeScript) {
			t.Fatalf("ran %q after the wait ran out", c)
		}
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
