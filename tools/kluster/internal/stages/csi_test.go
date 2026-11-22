package stages

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/nodeset"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile("../../../../" + path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The repository's CSI files agree with the hcloud site of every
// environment's Node set, so cp init applies them as they are.
func TestCSIManifestsAgreeWithTheNodeSets(t *testing.T) {
	csi := readRepoFile(t, "core/hcloud/csi.yaml")
	classes := readRepoFile(t, "core/hcloud/storageclasses.yaml")
	for _, env := range []string{"live", "rehearsal"} {
		set, err := nodeset.Load("../../../../infra/nodes/" + env + ".yaml")
		if err != nil {
			t.Fatal(err)
		}
		c := CSI{Manifest: csi, StorageClasses: classes, Location: set.Hcloud().Name}
		if err := c.checkManifests(); err != nil {
			t.Errorf("%s: %v", env, err)
		}
	}
}

func testClass(name, extra string) string {
	return "apiVersion: storage.k8s.io/v1\nkind: StorageClass\nmetadata:\n  name: " + name + "\n" + extra
}

const (
	defaultAnnotation = "  annotations:\n    storageclass.kubernetes.io/is-default-class: \"true\"\n"
	hel1Topology      = "allowedTopologies:\n  - matchLabelExpressions:\n      - key: csi.hetzner.cloud/location\n        values: [hel1]\n"
	csiWithoutClass   = "apiVersion: apps/v1\nkind: DaemonSet\nmetadata:\n  name: hcloud-csi-node\n"
)

func goodClasses() string {
	return testClass(defaultClass, defaultAnnotation+hel1Topology) + "---\n" + testClass(retainClass, hel1Topology)
}

func TestCSIActRefusesManifestsThatDisagree(t *testing.T) {
	for _, tc := range []struct {
		name string
		csi  CSI
		want string
	}{
		{"no location", CSI{Manifest: csiWithoutClass, StorageClasses: goodClasses()},
			"csi: no hcloud site location to allow"},
		{"chart class left on", CSI{Manifest: csiWithoutClass + "---\n" + testClass(defaultClass, defaultAnnotation), StorageClasses: goodClasses(), Location: "hel1"},
			"core/hcloud/csi.yaml carries a StorageClass; csi-values.yaml must set storageClasses: []"},
		{"retain class missing", CSI{Manifest: csiWithoutClass, StorageClasses: testClass(defaultClass, defaultAnnotation+hel1Topology), Location: "hel1"},
			"core/hcloud/storageclasses.yaml has no StorageClass hcloud-volumes-retain"},
		{"other location", CSI{Manifest: csiWithoutClass, StorageClasses: goodClasses(), Location: "fsn1"},
			"core/hcloud/storageclasses.yaml: hcloud-volumes allows csi.hetzner.cloud/location [hel1], but the hcloud site is fsn1"},
		{"no topology", CSI{Manifest: csiWithoutClass, StorageClasses: testClass(defaultClass, defaultAnnotation+hel1Topology) + "---\n" + testClass(retainClass, ""), Location: "hel1"},
			"core/hcloud/storageclasses.yaml: hcloud-volumes-retain allows csi.hetzner.cloud/location [], but the hcloud site is hel1"},
		{"two locations", CSI{Manifest: csiWithoutClass, StorageClasses: strings.Replace(goodClasses(), "[hel1]", "[hel1, nbg1]", 1), Location: "hel1"},
			"core/hcloud/storageclasses.yaml: hcloud-volumes allows csi.hetzner.cloud/location [hel1 nbg1], but the hcloud site is hel1"},
		{"no default", CSI{Manifest: csiWithoutClass, StorageClasses: testClass(defaultClass, hel1Topology) + "---\n" + testClass(retainClass, hel1Topology), Location: "hel1"},
			"core/hcloud/storageclasses.yaml: hcloud-volumes is not the default class"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sh := &scriptHost{}
			err := tc.csi.Act(context.Background(), &stage.Host{Exec: sh})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if len(sh.ran) > 0 || len(sh.written) > 0 {
				t.Fatalf("a refused Act touched the host: ran %v, wrote %v", sh.ran, sh.written)
			}
		})
	}
}

// Act writes both files and applies the driver, then the classes, before it
// waits for the node pods. It never deletes a class: kubectl apply changes
// allowedTopologies in place.
func TestCSIActAppliesBothFiles(t *testing.T) {
	sh := &scriptHost{}
	c := CSI{Manifest: csiWithoutClass, StorageClasses: goodClasses(), Location: "hel1"}
	if err := c.Act(context.Background(), &stage.Host{Exec: sh}); err != nil {
		t.Fatal(err)
	}
	if sh.written[manifestDir+"/csi.yaml"] != csiWithoutClass || sh.written[manifestDir+"/storageclasses.yaml"] != goodClasses() {
		t.Fatalf("manifests not written as given: %v", sh.written)
	}
	script := sh.written[scriptDir+"/csi.sh"]
	csi := strings.Index(script, "kubectl apply -f "+manifestDir+"/csi.yaml")
	classes := strings.Index(script, "kubectl apply -f "+manifestDir+"/storageclasses.yaml")
	wait := strings.Index(script, "rollout status ds/hcloud-csi-node")
	if csi < 0 || classes < csi || wait < classes {
		t.Fatalf("want apply csi.yaml, apply storageclasses.yaml, then wait:\n%s", script)
	}
	if strings.Contains(script, "delete") || strings.Contains(script, "replace") {
		t.Fatalf("the script deletes or replaces:\n%s", script)
	}
}

// The probe in real bash with a stub kubectl: jsonpath filters and quoting
// are tested, not only their text.
func TestCSIProbeInBash(t *testing.T) {
	stub := func(retainTopology, affinity string) stubbedBash {
		return newStub(t, `case "$*" in
  *'is-default-class'*) printf %s true ;;
  *'sc hcloud-volumes-retain'*allowedTopologies*) printf %s '`+retainTopology+`' ;;
  *'sc hcloud-volumes '*allowedTopologies*) printf %s hel1 ;;
  *'ds hcloud-csi-node'*affinity*) printf %s '`+affinity+`' ;;
esac
`)
	}
	c := CSI{Location: "hel1"}
	ctx := context.Background()
	for _, tc := range []struct {
		name, retain, affinity string
		done                   bool
		detail                 string
	}{
		{"as in the files", "hel1", "In/cloud", true, ""},
		{"retain class without topology", "", "In/cloud", false, "not yet: topology-hcloud-volumes-retain"},
		{"retain class in another location", "fsn1", "In/cloud", false, "not yet: topology-hcloud-volumes-retain"},
		{"chart's default affinity", "hel1", "NotIn/robot", false, "not yet: csi-node-affinity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := c.Probe(ctx, &stage.Host{Exec: stub(tc.retain, tc.affinity)})
			if err != nil || st.Done != tc.done || (!tc.done && st.Detail != tc.detail) {
				t.Fatalf("got %+v, %v", st, err)
			}
		})
	}
	// An empty Location never matches an empty live topology.
	st, err := CSI{}.Probe(ctx, &stage.Host{Exec: stub("", "In/cloud")})
	if err != nil || st.Done {
		t.Fatalf("no location, want not done: %+v, %v", st, err)
	}
}
