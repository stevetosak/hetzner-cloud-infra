package cmd

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/meshstore"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/nodeset"
)

const byoNodeSet = meshNodeSet + "  tswk1: {site: ts1, provisioner: byo, meshAddress: 10.101.1.1}\n"

func TestProvisionerFor(t *testing.T) {
	set := parseMeshSet(t, byoNodeSet)
	for _, c := range []struct {
		name, asked, want, err string
	}{
		{"tswk1", "", "byo", ""},
		{"tswk1", "byo", "byo", ""},
		{"k8swk1", "", "hcloud", ""},
		{"k8swk4", "", "hcloud", ""},
		{"tswk2", "byo", "byo", ""},
		{"tswk1", "hcloud", "", "declares tswk1 with provisioner byo, not hcloud"},
		{"tswk2", "ovh", "", `provisioner "ovh" is not one of [byo hcloud]`},
	} {
		p, kind, err := provisionerFor(set, c.name, c.asked)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s %q: got %v, want %q", c.name, c.asked, err, c.err)
			}
			continue
		}
		if err != nil || kind != c.want || p != provisioners[c.want] {
			t.Errorf("%s %q: got %s, %v; want %s", c.name, c.asked, kind, err, c.want)
		}
	}
}

func TestAddFlagsBelongToOneProvisioner(t *testing.T) {
	for _, c := range []struct {
		f    addFlags
		kind string
		err  string
	}{
		{addFlags{serverType: "cpx22"}, "hcloud", ""},
		{addFlags{site: "ts1", bootstrap: "192.168.1.5", hostKey: "SHA256:x"}, "byo", ""},
		{addFlags{serverType: "cpx22"}, "byo", "--server-type: not a flag of provisioner byo"},
		{addFlags{site: "ts1", hostKey: "SHA256:x"}, "hcloud", "--host-key, --site: not a flag of provisioner hcloud"},
	} {
		err := c.f.check(c.kind)
		if (c.err == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), c.err) {
			t.Errorf("%+v as %s: got %v, want %q", c.f, c.kind, err, c.err)
		}
	}
}

func TestCheckByoReach(t *testing.T) {
	if err := checkByoReach("tswk1", nodeset.Site{Name: "ts1", Reach: "nat"}); err != nil {
		t.Errorf("a NAT site: %v", err)
	}
	if err := checkByoReach("vps1", nodeset.Site{Name: "ovh1", Reach: "public"}); err == nil || !strings.Contains(err.Error(), "whose reach is public") {
		t.Errorf("a public site: %v", err)
	}
}

func TestCheckMeshMembers(t *testing.T) {
	m := parseMeshSet(t, byoNodeSet).Mesh()
	joined := map[string]meshstore.Entry{"k8s-cp": {}, "k8swk1": {}, "k8swk2": {}}
	nodes := func(names ...string) []kube.Node {
		var n []kube.Node
		for _, name := range names {
			n = append(n, kube.Node{Name: name})
		}
		return n
	}
	if err := checkMeshMembers(nodes("k8s-cp", "k8swk1", "k8swk2", "tswk1"), m, joined, "tswk1"); err != nil {
		t.Errorf("every other Node joined: %v", err)
	}
	notJoined := map[string]meshstore.Entry{"k8s-cp": {}, "k8swk1": {}}
	err := checkMeshMembers(nodes("k8s-cp", "k8swk1", "k8swk2", "k8swk3"), m, notJoined, "tswk1")
	if err == nil || !strings.Contains(err.Error(), "the Nodes [k8swk2 k8swk3] have not joined the mesh") {
		t.Errorf("a Node not joined and one not declared: %v", err)
	}
}

func TestCheckOwnNode(t *testing.T) {
	m, _ := parseMeshSet(t, byoNodeSet).Mesh().Machine("tswk1")
	if err := checkOwnNode([]kube.Node{{Name: "tswk1", ProviderID: "kluster://ts1/tswk1"}}, m); err != nil {
		t.Errorf("its own Node: %v", err)
	}
	if err := checkOwnNode([]kube.Node{{Name: "tswk1", ProviderID: "hcloud://42"}}, m); err == nil {
		t.Error("a Hetzner Node by the machine's name passed")
	}
}

func TestByoKubelet(t *testing.T) {
	m, _ := parseMeshSet(t, byoNodeSet).Mesh().Machine("tswk1")
	got := byoKubelet(m).String()
	want := "KUBELET_EXTRA_ARGS=--node-ip=10.101.1.1 --provider-id=kluster://ts1/tswk1 " +
		"--register-with-taints=tosak.net/reliability=best-effort:NoSchedule " +
		"--node-labels=node.kubernetes.io/exclude-from-external-load-balancers=true"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	m.Site.Reliability = "high"
	if k := byoKubelet(m); len(k.Taints) != 0 {
		t.Errorf("a machine of high reliability registers taints %v", k.Taints)
	}
}

// The byo release is the Hetzner image's: the packages kluster pins are
// built for one release.
func TestByoReleaseIsTheWorkerImages(t *testing.T) {
	b, err := os.ReadFile("../../../infra/workers/variables.tf")
	if err != nil {
		t.Fatal(err)
	}
	image := regexp.MustCompile(`(?s)variable "image" \{.*?default\s*=\s*"ubuntu-([0-9.]+)"`).FindSubmatch(b)
	if image == nil || string(image[1]) != byoRelease {
		t.Fatalf("infra/workers image %q, byoRelease %s", image, byoRelease)
	}
}

func TestFingerprintShape(t *testing.T) {
	good := "SHA256:" + strings.Repeat("A", 43)
	for v, want := range map[string]bool{good: true, "SHA256:abc": false, "MD5:" + strings.Repeat("a", 43): false, good + " root@x": false} {
		if fingerprint.MatchString(v) != want {
			t.Errorf("%q matched %v", v, !want)
		}
	}
}
