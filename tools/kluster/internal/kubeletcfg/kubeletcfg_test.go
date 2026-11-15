package kubeletcfg

import (
	"os"
	"strings"
	"testing"

	"github.com/aymanbagabas/go-udiff"
)

// The reserve of ADR 0014, as config.Kubelet.Fields gives it.
var reserve = map[string]any{
	"kubeReserved":              map[string]string{"memory": "160Mi"},
	"systemReserved":            map[string]string{"memory": "480Mi"},
	"evictionSoft":              map[string]string{"memory.available": "300Mi"},
	"evictionSoftGracePeriod":   map[string]string{"memory.available": "1m"},
	"evictionMaxPodGracePeriod": 30,
}

// kubeadm.yaml is the live kubelet-config ConfigMap of 2026-11-15.
func kubeadmDoc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/kubeadm.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMergeAddsOnlyTheFieldsInKubeadmOrder(t *testing.T) {
	doc := kubeadmDoc(t)
	merged, err := Merge(doc, reserve)
	if err != nil {
		t.Fatal(err)
	}
	var added []string
	for _, l := range strings.Split(udiff.Unified("a", "b", doc, merged), "\n") {
		if strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---") {
			t.Errorf("Merge removed a line: %q", l)
		}
		if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") {
			added = append(added, l)
		}
	}
	want := []string{
		"+evictionMaxPodGracePeriod: 30",
		"+evictionSoft:",
		"+  memory.available: 300Mi",
		"+evictionSoftGracePeriod:",
		"+  memory.available: 1m",
		"+kubeReserved:",
		"+  memory: 160Mi",
		"+systemReserved:",
		"+  memory: 480Mi",
	}
	if strings.Join(added, "\n") != strings.Join(want, "\n") {
		t.Errorf("added:\n%s\nwant:\n%s", strings.Join(added, "\n"), strings.Join(want, "\n"))
	}
	// kubeReserved goes between kind and logging, as kubeadm sorts.
	if i, j, k := strings.Index(merged, "\nkind:"), strings.Index(merged, "\nkubeReserved:"), strings.Index(merged, "\nlogging:"); !(i < j && j < k) {
		t.Errorf("kubeReserved is not between kind and logging:\n%s", merged)
	}
}

func TestMergeReplacesAFieldWhereItStands(t *testing.T) {
	doc := "apiVersion: v1\nclusterDNS:\n- 10.96.0.10\nkubeReserved:\n  cpu: 100m\n  memory: 64Mi\nkind: KubeletConfiguration\n"
	merged, err := Merge(doc, map[string]any{"kubeReserved": map[string]string{"memory": "160Mi"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "apiVersion: v1\nclusterDNS:\n- 10.96.0.10\nkubeReserved:\n  memory: 160Mi\nkind: KubeletConfiguration\n"
	if merged != want {
		t.Errorf("got\n%s\nwant\n%s", merged, want)
	}
	// A second merge changes nothing.
	again, err := Merge(merged, map[string]any{"kubeReserved": map[string]string{"memory": "160Mi"}})
	if err != nil || again != merged {
		t.Errorf("not idempotent: %v\n%s", err, again)
	}
}

func TestDifferNamesWhatIsMissingOrOther(t *testing.T) {
	cfg, err := Parse(kubeadmDoc(t))
	if err != nil {
		t.Fatal(err)
	}
	d, err := Differ(cfg, reserve)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(d, ",") != "evictionMaxPodGracePeriod,evictionSoft,evictionSoftGracePeriod,kubeReserved,systemReserved" {
		t.Errorf("got %v", d)
	}
	merged, _ := Merge(kubeadmDoc(t), reserve)
	cfg, _ = Parse(merged)
	if d, _ := Differ(cfg, reserve); len(d) != 0 {
		t.Errorf("a merged config still differs in %v", d)
	}
	cfg["kubeReserved"] = map[string]any{"memory": "128Mi"}
	if d, _ := Differ(cfg, reserve); strings.Join(d, ",") != "kubeReserved" {
		t.Errorf("another value: got %v", d)
	}
}

// configz is JSON with numbers as floats; it compares as the YAML file does.
func TestConfigzComparesAsTheFile(t *testing.T) {
	raw := `{"kubeletconfig":{"kubeReserved":{"memory":"160Mi"},"systemReserved":{"memory":"480Mi"},` +
		`"evictionSoft":{"memory.available":"300Mi"},"evictionSoftGracePeriod":{"memory.available":"1m"},` +
		`"evictionMaxPodGracePeriod":30,"evictionHard":{"memory.available":"100Mi","nodefs.available":"10%"}}}`
	cfg, err := Configz(raw)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := Differ(cfg, reserve); len(d) != 0 {
		t.Errorf("configz differs in %v", d)
	}
	if _, err := Configz(`{"other":{}}`); err == nil {
		t.Error("configz without kubeletconfig was taken")
	}
}

func TestAllocatable(t *testing.T) {
	// The cx23 workers of 2026-11-15: 3911564Ki, allocatable 3809164Ki today.
	none := map[string]any{"evictionHard": map[string]any{"memory.available": "100Mi"}}
	for _, c := range []struct {
		name string
		cfg  map[string]any
		want int64 // Ki
	}{
		{"today, evictionHard set", none, 3809164},
		{"today, the kubelet's default", map[string]any{}, 3809164},
		{"with the reserve", mustWith(t, none, reserve), 3809164 - 640*1024},
		// 10 % of 3911564Ki is 391156.4Ki; the bytes round down.
		{"a percentage", map[string]any{"evictionHard": map[string]any{"memory.available": "10%"}}, 3520407},
	} {
		got, err := Allocatable("3911564Ki", c.cfg)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.Value()/1024 != c.want {
			t.Errorf("%s: got %dKi, want %dKi", c.name, got.Value()/1024, c.want)
		}
	}
	if _, err := Allocatable("lots", none); err == nil {
		t.Error("a capacity that is no quantity was taken")
	}
}

func mustWith(t *testing.T, cfg, want map[string]any) map[string]any {
	t.Helper()
	w, err := With(cfg, want)
	if err != nil {
		t.Fatal(err)
	}
	return w
}
