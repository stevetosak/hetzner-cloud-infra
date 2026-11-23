package nodelist

import (
	"bytes"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/nodeset"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

var hel1 = nodeset.Site{Name: "hel1", Provider: nodeset.ProviderHcloud, Reliability: "high", Reach: "public", BlockVolumes: "hcloud", LoadBalancer: "hcloud"}

func worker(name, private, vpn string) workerset.Worker {
	return workerset.Worker{Name: name, PrivateIP: private, VpnIP: vpn}
}

func server(name, private string) *cloud.Server {
	return &cloud.Server{Name: name, Status: "running", Location: "hel1", PrivateIPs: []string{private}}
}

func node(name, ip string, ready bool) kube.Node {
	return kube.Node{Name: name, InternalIP: ip, Ready: ready, Kubelet: "v1.37.1", Labels: hel1.Labels()}
}

func byName(rows []Row) map[string]Row {
	m := map[string]Row{}
	for _, r := range rows {
		m[r.Name] = r
	}
	return m
}

func TestCompareInStep(t *testing.T) {
	rows := Compare(Input{
		Set:          []workerset.Worker{worker("k8swk1", "10.0.2.6", "10.100.0.2")},
		Servers:      []*cloud.Server{server("k8s-cp", "10.0.1.5"), server("k8swk1", "10.0.2.6")},
		Nodes:        []kube.Node{{Name: "k8s-cp", ControlPlane: true, Ready: true}, node("k8swk1", "10.0.2.6", true)},
		NodesRead:    true,
		ControlPlane: "k8s-cp",
		Site:         hel1,
	})
	if len(rows) != 1 || rows[0].Name != "k8swk1" || HasDrift(rows) {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestCompareMarksEachDrift(t *testing.T) {
	stopped := server("k8swk3", "10.0.2.8")
	stopped.Status = "off"
	rows := byName(Compare(Input{
		Set: []workerset.Worker{
			worker("k8swk1", "10.0.2.6", "10.100.0.2"), // no server
			worker("k8swk2", "10.0.2.7", "10.100.0.3"), // server, not joined
			worker("k8swk3", "10.0.2.8", "10.100.0.4"), // off, NotReady
			worker("k8swk4", "10.0.2.9", "10.100.0.5"), // wrong private IP
		},
		Servers:   []*cloud.Server{server("k8swk2", "10.0.2.7"), stopped, server("k8swk4", "10.0.2.10"), server("stray", "10.0.2.20")},
		Nodes:     []kube.Node{node("k8swk3", "10.0.2.8", false), node("k8swk4", "10.0.2.10", true), node("ghost", "10.0.2.30", true)},
		NodesRead: true,
		Site:      hel1,
	}))
	want := map[string][]string{
		"k8swk1": {"no server"},
		"k8swk2": {"not joined"},
		"k8swk3": {"server off", "NotReady"},
		"k8swk4": {"server private IP [10.0.2.10], set 10.0.2.9", "Node InternalIP 10.0.2.10, set 10.0.2.9"},
		"stray":  {"server not in the Worker set", "not joined"},
		"ghost":  {"Node without a server"},
	}
	for name, w := range want {
		if got := rows[name].Drift; !slices.Equal(got, w) {
			t.Errorf("%s: drift %q, want %q", name, got, w)
		}
	}
}

// In a rehearsal with bootstrap SSH closed the Nodes are not read: that is
// unknown, not "not joined".
func TestCompareWithoutNodesMarksNoNodeDrift(t *testing.T) {
	in := Input{
		Set:     []workerset.Worker{worker("k8swk1", "10.0.2.6", "10.100.0.2")},
		Servers: []*cloud.Server{server("k8swk1", "10.0.2.6")},
		Site:    hel1,
	}
	rows := Compare(in)
	if HasDrift(rows) {
		t.Fatalf("drift without Nodes: %+v", rows)
	}
	var out bytes.Buffer
	if err := Print(&out, rows, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "?") {
		t.Errorf("the Node column must show unknown:\n%s", out.String())
	}
}

// A Node whose labels differ from its site's profile, or a server outside
// the site's location, is drift; labels of other owners are not.
func TestCompareMarksProfileDrift(t *testing.T) {
	stale := node("k8swk2", "10.0.2.7", true)
	stale.Labels = map[string]string{
		nodeset.Prefix + nodeset.KeyProvider:     "hcloud",
		nodeset.Prefix + nodeset.KeySite:         "hel1",
		nodeset.Prefix + nodeset.KeyReliability:  "high",
		nodeset.Prefix + nodeset.KeyReach:        "nat",
		nodeset.Prefix + nodeset.KeyBlockVolumes: "hcloud",
		nodeset.Prefix + "gpu":                   "none",
		"kubernetes.io/hostname":                 "k8swk2",
	}
	bare := node("k8swk3", "10.0.2.8", true)
	bare.Labels = map[string]string{"kubernetes.io/hostname": "k8swk3"}
	moved := server("k8swk4", "10.0.2.9")
	moved.Location = "fsn1"
	rows := Compare(Input{
		Set: []workerset.Worker{
			worker("k8swk1", "10.0.2.6", "10.100.0.2"),
			worker("k8swk2", "10.0.2.7", "10.100.0.3"),
			worker("k8swk3", "10.0.2.8", "10.100.0.4"),
			worker("k8swk4", "10.0.2.9", "10.100.0.5"),
		},
		Servers:   []*cloud.Server{server("k8swk1", "10.0.2.6"), server("k8swk2", "10.0.2.7"), server("k8swk3", "10.0.2.8"), moved},
		Nodes:     []kube.Node{node("k8swk1", "10.0.2.6", true), stale, bare, node("k8swk4", "10.0.2.9", true)},
		NodesRead: true,
		Site:      hel1,
	})
	got := byName(rows)
	want := map[string][]string{
		"k8swk1": nil,
		"k8swk2": {"profile: set load-balancer=hcloud, reach=public; remove gpu"},
		"k8swk3": {"profile: set block-volumes=hcloud, load-balancer=hcloud, provider=hcloud, reach=public, reliability=high, site=hel1"},
		"k8swk4": {"server in fsn1, site hel1"},
	}
	for name, w := range want {
		if d := got[name].Drift; !slices.Equal(d, w) {
			t.Errorf("%s: drift %q, want %q", name, d, w)
		}
	}
	var out bytes.Buffer
	if err := Print(&out, rows, true); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out.String(), "\n")
	if !strings.Contains(lines[0], "PROFILE") {
		t.Errorf("no PROFILE column:\n%s", out.String())
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "k8swk1 ") && !strings.Contains(l, " hel1 ") {
			t.Errorf("k8swk1 must show its site hel1: %q", l)
		}
		if strings.HasPrefix(l, "k8swk3 ") && strings.Contains(l, " hel1 ") {
			t.Errorf("k8swk3 carries no site label: %q", l)
		}
	}
}

var ts1 = nodeset.Site{Name: "ts1", Provider: "self-hosted", Reliability: "best-effort", Reach: "nat"}

var tswk1 = nodeset.Machine{Name: "tswk1", Site: ts1, Provisioner: nodeset.ProvisionerByo, Address: netip.MustParseAddr("10.101.1.1")}

// byoNode is tswk1's Node as a good build leaves it.
func byoNode() kube.Node {
	return kube.Node{Name: "tswk1", InternalIP: "10.101.1.1", Ready: true, Kubelet: "v1.37.0", ProviderID: "kluster://ts1/tswk1",
		Labels: ts1.Labels(), Taints: []string{nodeset.ReliabilityTaint.String()}}
}

func byoRow(t *testing.T, n *kube.Node, servers ...*cloud.Server) Row {
	t.Helper()
	in := Input{Servers: servers, NodesRead: true, ControlPlane: "k8s-cp", Site: hel1, Byo: []nodeset.Machine{tswk1}}
	if n != nil {
		in.Nodes = []kube.Node{*n}
	}
	r, ok := byName(Compare(in))["tswk1"]
	if !ok {
		t.Fatal("no row for tswk1")
	}
	return r
}

// A byo machine has no Worker set entry and no server, and that is no drift.
func TestCompareByoInStep(t *testing.T) {
	n := byoNode()
	if r := byoRow(t, &n); len(r.Drift) != 0 {
		t.Fatalf("drift %v", r.Drift)
	}
	var out bytes.Buffer
	if err := Print(&out, []Row{byoRow(t, &n)}, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "byo ts1, mesh 10.101.1.1") {
		t.Errorf("table:\n%s", out.String())
	}
}

func TestCompareMarksByoDrift(t *testing.T) {
	if r := byoRow(t, nil); !slices.Equal(r.Drift, []string{"not joined"}) {
		t.Errorf("no Node: %v", r.Drift)
	}
	n := byoNode()
	n.InternalIP, n.ProviderID, n.Taints = "192.168.1.5", "", nil
	want := []string{"Node InternalIP 192.168.1.5, mesh address 10.101.1.1", `providerID "", want kluster://ts1/tswk1`,
		"profile: add taint tosak.net/reliability=best-effort:NoSchedule"}
	if r := byoRow(t, &n); !slices.Equal(r.Drift, want) {
		t.Errorf("drift %q\nwant  %q", r.Drift, want)
	}
	good := byoNode()
	if r := byoRow(t, &good, server("tswk1", "10.0.2.9")); !slices.Contains(r.Drift, "a Hetzner server has its name") {
		t.Errorf("a server by its name: %v", r.Drift)
	}
}
