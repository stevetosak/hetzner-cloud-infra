package stages

import (
	"context"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/nodeset"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

const labelNode = "KUBECONFIG=/etc/kubernetes/admin.conf kubectl 'label' 'node'"

var hel1 = nodeset.Site{Name: "hel1", Provider: "hcloud", Reliability: "high", Reach: "public", LoadBalancer: "hcloud"}

// profiledNodes is a Node list where k8swk1 carries labels; role=worker
// is a label of someone else.
func profiledNodes(labels string) string {
	return `{"items":[{"metadata":{"name":"k8s-cp"}},{"metadata":{"name":"k8swk1","labels":{"role":"worker"` + labels + `}}}]}`
}

const fullProfile = `,"tosak.net.node-restriction.kubernetes.io/provider":"hcloud",
 "tosak.net.node-restriction.kubernetes.io/site":"hel1",
 "tosak.net.node-restriction.kubernetes.io/reliability":"high",
 "tosak.net.node-restriction.kubernetes.io/reach":"public",
 "tosak.net.node-restriction.kubernetes.io/load-balancer":"hcloud"`

func TestNodeProfileProbe(t *testing.T) {
	p := NodeProfile{Node: "k8swk1", Site: hel1}
	for _, c := range []struct {
		name, labels string
		done         bool
		detail       string
	}{
		{"no profile", "", false, "not yet: set load-balancer=hcloud, provider=hcloud, reach=public, reliability=high, site=hel1"},
		{"full profile", fullProfile, true, "k8swk1 carries the profile of site hel1"},
		{"stale key", fullProfile + `,"tosak.net.node-restriction.kubernetes.io/block-volumes":"hcloud"`, false,
			"not yet: remove block-volumes"},
		{"other site", strings.Replace(fullProfile, `site":"hel1"`, `site":"ts1"`, 1), false, "not yet: set site=hel1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := &stage.Host{Exec: &scriptHost{out: map[string]string{getNodes: profiledNodes(c.labels)}}}
			st, err := p.Probe(context.Background(), h)
			if err != nil {
				t.Fatal(err)
			}
			if st.Done != c.done || st.Detail != c.detail {
				t.Errorf("got %+v, want done %v %q", st, c.done, c.detail)
			}
		})
	}
}

// Act labels only what differs, and never names a label outside the prefix.
func TestNodeProfileActChangesOnlyTheDiff(t *testing.T) {
	stale := strings.Replace(fullProfile, `reach":"public"`, `reach":"nat"`, 1) +
		`,"tosak.net.node-restriction.kubernetes.io/block-volumes":"hcloud"`
	sh := &scriptHost{out: map[string]string{getNodes: profiledNodes(stale)}}
	if err := (NodeProfile{Node: "k8swk1", Site: hel1}).Act(context.Background(), &stage.Host{Exec: sh}); err != nil {
		t.Fatal(err)
	}
	want := labelNode + " 'k8swk1' '--overwrite' 'tosak.net.node-restriction.kubernetes.io/reach=public' " +
		"'tosak.net.node-restriction.kubernetes.io/block-volumes-'"
	if got := sh.ran[len(sh.ran)-1]; got != want {
		t.Errorf("ran\n%s\nwant\n%s", got, want)
	}
}

func TestNodeProfileActOnADoneNodeRunsNoLabel(t *testing.T) {
	sh := &scriptHost{out: map[string]string{getNodes: profiledNodes(fullProfile)}}
	if err := (NodeProfile{Node: "k8swk1", Site: hel1}).Act(context.Background(), &stage.Host{Exec: sh}); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range sh.ran {
		if strings.HasPrefix(cmd, labelNode) {
			t.Errorf("a done Node got %s", cmd)
		}
	}
}

func TestNodeProfileMissingNodeIsAnError(t *testing.T) {
	h := &stage.Host{Exec: &scriptHost{out: map[string]string{getNodes: profiledNodes("")}}}
	_, err := NodeProfile{Node: "k8swk9", Site: hel1}.Probe(context.Background(), h)
	if err == nil || !strings.Contains(err.Error(), "no Node k8swk9") {
		t.Fatalf("want no Node k8swk9, got %v", err)
	}
}

// A Node that does not exist yet previews its whole profile.
func TestNodeProfilePreviewForANewNode(t *testing.T) {
	got, err := NodeProfile{Node: "k8swk4", Site: hel1}.Preview(context.Background(), &stage.Host{Name: "k8swk4"})
	if err != nil {
		t.Fatal(err)
	}
	want := "kubectl label node k8swk4 --overwrite tosak.net.node-restriction.kubernetes.io/load-balancer=hcloud " +
		"tosak.net.node-restriction.kubernetes.io/provider=hcloud tosak.net.node-restriction.kubernetes.io/reach=public " +
		"tosak.net.node-restriction.kubernetes.io/reliability=high tosak.net.node-restriction.kubernetes.io/site=hel1\n"
	if got != want {
		t.Errorf("preview\n%s\nwant\n%s", got, want)
	}
}

var ts1 = nodeset.Site{Name: "ts1", Provider: "self-hosted", Reliability: "best-effort", Reach: "nat"}

const taintNode = "KUBECONFIG=/etc/kubernetes/admin.conf kubectl 'taint' 'node'"

// taintedNodes is a Node list where node carries the full profile of site
// and the taints given as JSON objects.
func taintedNodes(node string, site nodeset.Site, taints string) string {
	labels := ""
	for k, v := range site.Labels() {
		labels += `,"` + k + `":"` + v + `"`
	}
	return `{"items":[{"metadata":{"name":"` + node + `","labels":{"role":"worker"` + labels + `}},"spec":{"taints":[` + taints + `]}}]}`
}

const (
	bestEffortTaint = `{"key":"tosak.net/reliability","value":"best-effort","effect":"NoSchedule"}`
	otherTaint      = `{"key":"example.com/gpu","effect":"NoSchedule"}`
)

func TestNodeProfileTaint(t *testing.T) {
	for _, c := range []struct {
		name   string
		site   nodeset.Site
		taints string
		detail string
		ran    []string
	}{
		{"best-effort without", ts1, otherTaint, "not yet: add taint tosak.net/reliability=best-effort:NoSchedule",
			[]string{taintNode + " 'tswk1' 'tosak.net/reliability=best-effort:NoSchedule' '--overwrite'"}},
		{"best-effort with", ts1, otherTaint + "," + bestEffortTaint, "tswk1 carries the profile of site ts1", nil},
		{"best-effort, other effect too", ts1, bestEffortTaint + `,{"key":"tosak.net/reliability","value":"best-effort","effect":"NoExecute"}`,
			"not yet: remove taint tosak.net/reliability; add taint tosak.net/reliability=best-effort:NoSchedule",
			[]string{taintNode + " 'tswk1' 'tosak.net/reliability-'", taintNode + " 'tswk1' 'tosak.net/reliability=best-effort:NoSchedule' '--overwrite'"}},
		{"high with", hel1, otherTaint + "," + bestEffortTaint, "not yet: remove taint tosak.net/reliability",
			[]string{taintNode + " 'tswk1' 'tosak.net/reliability-'"}},
		{"high without", hel1, otherTaint, "tswk1 carries the profile of site hel1", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := NodeProfile{Node: "tswk1", Site: c.site}
			sh := &scriptHost{out: map[string]string{getNodes: taintedNodes("tswk1", c.site, c.taints)}}
			st, err := p.Probe(context.Background(), &stage.Host{Exec: sh})
			if err != nil {
				t.Fatal(err)
			}
			if st.Detail != c.detail {
				t.Errorf("probe %q, want %q", st.Detail, c.detail)
			}
			if err := p.Act(context.Background(), &stage.Host{Exec: sh}); err != nil {
				t.Fatal(err)
			}
			var ran []string
			for _, cmd := range sh.ran {
				if strings.HasPrefix(cmd, taintNode) || strings.HasPrefix(cmd, labelNode) {
					ran = append(ran, cmd)
				}
			}
			if strings.Join(ran, "\n") != strings.Join(c.ran, "\n") {
				t.Errorf("ran\n%s\nwant\n%s", strings.Join(ran, "\n"), strings.Join(c.ran, "\n"))
			}
		})
	}
}

// A new Node of a best-effort site previews its taint with its labels.
func TestNodeProfilePreviewForANewBestEffortNode(t *testing.T) {
	got, err := NodeProfile{Node: "tswk1", Site: ts1}.Preview(context.Background(), &stage.Host{Name: "tswk1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "\nkubectl taint node tswk1 tosak.net/reliability=best-effort:NoSchedule --overwrite\n") {
		t.Errorf("preview\n%s", got)
	}
}
