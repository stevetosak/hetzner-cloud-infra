package nodeset

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// labelKey is a qualified label key: a DNS subdomain prefix, a slash and a
// name of at most 63 characters (the Kubernetes label syntax).
var labelKey = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?/[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$`)

const twoSites = `
sites:
  hel1:
    provider: hcloud
    reliability: high
    reach: public
    blockVolumes: hcloud
    loadBalancer: hcloud
  ts1:
    provider: self-hosted
    reliability: best-effort
    reach: nat
`

func TestParseTwoSites(t *testing.T) {
	s, err := Parse("live.yaml", []byte(twoSites))
	if err != nil {
		t.Fatal(err)
	}
	h := s.Hcloud()
	want := Site{Name: "hel1", Provider: "hcloud", Reliability: "high", Reach: "public", BlockVolumes: "hcloud", LoadBalancer: "hcloud"}
	if h != want {
		t.Errorf("Hcloud() = %+v, want %+v", h, want)
	}
}

// Each case changes one line of a valid file, so the refusal can only come
// from that line.
func TestParseRefuses(t *testing.T) {
	for name, tc := range map[string]struct{ old, new, want string }{
		"unknown field":      {"reach: nat", "reach: nat\n    gpu: true", "field gpu not found"},
		"name not a label":   {"  ts1:", "  TS_1:", `site "TS_1": the name is not a DNS label`},
		"no provider":        {"provider: self-hosted", `provider: ""`, "site ts1: provider is required"},
		"provider not label": {"provider: self-hosted", "provider: Self Hosted", `provider "Self Hosted" is not a DNS label`},
		"reliability":        {"reliability: best-effort", "reliability: home", `reliability "home" must be one of high, best-effort`},
		"reach":              {"reach: nat", "reach: private", `reach "private" must be one of public, nat`},
		"capability kind":    {"loadBalancer: hcloud", "loadBalancer: true", ""},
		"capability label":   {"blockVolumes: hcloud", "blockVolumes: Hetzner Volumes", `blockVolumes "Hetzner Volumes" is not a DNS label`},
		"no hcloud site":     {"provider: hcloud", "provider: other", "exactly one site has provider hcloud"},
		"two hcloud sites":   {"provider: self-hosted", "provider: hcloud", "found 2 [hel1 ts1]"},
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(twoSites, tc.old) {
				t.Fatalf("the file no longer holds %q", tc.old)
			}
			_, err := Parse("live.yaml", []byte(strings.Replace(twoSites, tc.old, tc.new, 1)))
			if tc.want == "" {
				// A YAML bool is the string "true" to a string field, and a
				// valid kind name: the label value would read "true".
				if err != nil {
					t.Fatalf("got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal naming %q, got %v", tc.want, err)
			}
		})
	}
}

func TestParseRefusesAnEmptyFile(t *testing.T) {
	if _, err := Parse("live.yaml", nil); err == nil {
		t.Fatal("an empty file was accepted")
	}
	if _, err := Parse("live.yaml", []byte("sites: {}\n")); err == nil || !strings.Contains(err.Error(), "found 0") {
		t.Fatalf("a set with no site: got %v", err)
	}
}

func TestLabels(t *testing.T) {
	s, err := Parse("live.yaml", []byte(twoSites))
	if err != nil {
		t.Fatal(err)
	}
	full := s.sites["hel1"].Labels()
	if got := slices.Sorted(maps.Keys(full)); !slices.Equal(got, []string{
		Prefix + "block-volumes", Prefix + "load-balancer", Prefix + "provider",
		Prefix + "reach", Prefix + "reliability", Prefix + "site",
	}) {
		t.Errorf("hel1 keys = %v", got)
	}
	bare := s.sites["ts1"].Labels()
	want := map[string]string{Prefix + "provider": "self-hosted", Prefix + "site": "ts1",
		Prefix + "reliability": "best-effort", Prefix + "reach": "nat"}
	if !maps.Equal(bare, want) {
		t.Errorf("ts1 labels = %v, want %v (no label for a capability the site lacks)", bare, want)
	}
	for k, v := range full {
		if !labelKey.MatchString(k) {
			t.Errorf("key %s is not a qualified label key", k)
		}
		if !dnsLabel.MatchString(v) {
			t.Errorf("value %s is not a valid label value", v)
		}
	}
}

func TestDiff(t *testing.T) {
	want := map[string]string{Prefix + "site": "hel1", Prefix + "reach": "public"}
	have := map[string]string{
		Prefix + "site":                         "hel1",
		Prefix + "reach":                        "nat",
		Prefix + "load-balancer":                "hcloud",
		"topology.kubernetes.io/region":         "hel1",
		"node-role.kubernetes.io/control-plane": "",
	}
	c := Diff(have, want)
	if !maps.Equal(c.Set, map[string]string{Prefix + "reach": "public"}) {
		t.Errorf("set = %v", c.Set)
	}
	if !slices.Equal(c.Remove, []string{Prefix + "load-balancer"}) {
		t.Errorf("remove = %v: only keys under the prefix are kluster's", c.Remove)
	}
	if got := c.String(); got != "set reach=public; remove load-balancer" {
		t.Errorf("String() = %q", got)
	}
	if !Diff(want, want).Empty() {
		t.Error("the same labels differ")
	}
	if got := Diff(nil, want).String(); got != "set reach=public, site=hel1" {
		t.Errorf("a bare Node: %q", got)
	}
}
