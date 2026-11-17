package cmd

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/nodeset"
)

var hel1Site = nodeset.Site{Name: "hel1", Provider: "hcloud", Reliability: "high", Reach: "public"}

func profileNodes(names ...string) []kube.Node {
	var nodes []kube.Node
	for _, n := range names {
		nodes = append(nodes, kube.Node{Name: n})
	}
	return nodes
}

func hetzner(servers ...*cloud.Server) hetznerNodes {
	return hetznerNodes{controlPlane: "k8s-cp", workers: []string{"k8swk1", "k8swk2"}, servers: servers, site: hel1Site}
}

func names(t []error) string { return fmt.Sprint(t) }

func TestProfileTargetsEveryHetznerNode(t *testing.T) {
	h := hetzner(&cloud.Server{Name: "k8s-cp", Location: "hel1"}, &cloud.Server{Name: "k8swk1", Location: "hel1"},
		&cloud.Server{Name: "k8swk2", Location: "hel1"})
	targets, problems := h.targets(profileNodes("k8s-cp", "k8swk1", "k8swk2"), "")
	if len(problems) > 0 {
		t.Fatalf("problems: %v", problems)
	}
	var got []string
	for _, tg := range targets {
		if tg.Site != hel1Site {
			t.Errorf("%s gets site %+v", tg.Node, tg.Site)
		}
		got = append(got, tg.Node)
	}
	if strings.Join(got, " ") != "k8s-cp k8swk1 k8swk2" {
		t.Errorf("targets %v", got)
	}
}

func TestProfileTargetsRefuses(t *testing.T) {
	for _, c := range []struct {
		name    string
		h       hetznerNodes
		nodes   []kube.Node
		only    string
		targets int
		want    string
	}{
		{"unknown node", hetzner(&cloud.Server{Name: "k8swk1", Location: "hel1"}), profileNodes("k8swk1", "ts1-a"), "", 1,
			"Node ts1-a is neither k8s-cp nor in the Worker set"},
		{"no server", hetzner(), profileNodes("k8swk1"), "", 0, "Node k8swk1 has no Hetzner server"},
		{"other location", hetzner(&cloud.Server{Name: "k8swk1", Location: "fsn1"}), profileNodes("k8swk1"), "", 0,
			`k8swk1 stands in "fsn1", but the Node set's hcloud site is hel1`},
		{"named node missing", hetzner(&cloud.Server{Name: "k8swk1", Location: "hel1"}), profileNodes("k8swk1"), "k8swk2", 0,
			"the cluster has no Node k8swk2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			targets, problems := c.h.targets(c.nodes, c.only)
			if len(targets) != c.targets {
				t.Errorf("targets %+v, want %d", targets, c.targets)
			}
			if len(problems) != 1 || !strings.Contains(problems[0].Error(), c.want) {
				t.Errorf("problems %s, want one naming %q", names(problems), c.want)
			}
		})
	}
}

func TestProfileTargetsOnlyTheNamedNode(t *testing.T) {
	h := hetzner(&cloud.Server{Name: "k8swk1", Location: "hel1"}, &cloud.Server{Name: "k8swk2", Location: "hel1"})
	targets, problems := h.targets(profileNodes("k8swk1", "k8swk2", "ts1-a"), "k8swk2")
	if len(problems) > 0 || len(targets) != 1 || targets[0].Node != "k8swk2" {
		t.Fatalf("targets %+v, problems %v", targets, problems)
	}
}

func TestCheckSite(t *testing.T) {
	if err := checkSite(&cloud.Server{Name: "k8s-cp", Location: "hel1"}, hel1Site); err != nil {
		t.Errorf("hel1 in hel1: %v", err)
	}
	if err := checkSite(&cloud.Server{Name: "k8s-cp", Location: "nbg1"}, hel1Site); err == nil {
		t.Error("a server in nbg1 passed the hel1 site")
	}
}
