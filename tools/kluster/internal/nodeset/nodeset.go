// Package nodeset reads an environment's Node set file: every site the
// cluster's Nodes stand on, and the capability profile of each (ADR 0016,
// Amendments). kluster writes a site's profile on each of its Nodes as admin
// labels under Prefix, which a kubelet cannot set or change
// (NodeRestriction), so a placement rule can trust them.
package nodeset

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Prefix is the admin label prefix of a profile. The NodeRestriction
// admission plugin refuses any label under *.node-restriction.kubernetes.io/
// from a kubelet.
const Prefix = "tosak.net.node-restriction.kubernetes.io/"

// The label keys of a profile, without Prefix.
const (
	KeyProvider     = "provider"
	KeySite         = "site"
	KeyReliability  = "reliability"
	KeyReach        = "reach"
	KeyBlockVolumes = "block-volumes"
	KeyLoadBalancer = "load-balancer"
)

// ProviderHcloud is the provider of the Hetzner Cloud servers: the Control
// Plane and the Worker set belong to the one site that has it.
const ProviderHcloud = "hcloud"

// dnsLabel is a site name, a provider or a capability kind: a valid label
// value that reads the same everywhere.
var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// The values a site's reliability and reach can take.
var (
	reliabilities = []string{"high", "best-effort"}
	reaches       = []string{"public", "nat"}
)

// Site is one place Nodes stand on, and what a workload can count on there.
// An empty BlockVolumes or LoadBalancer means the site has none; a set one
// names its kind (hcloud), because a workload needs that kind, not any.
type Site struct {
	Name         string `yaml:"-"`
	Provider     string `yaml:"provider"`
	Reliability  string `yaml:"reliability"`
	Reach        string `yaml:"reach"`
	BlockVolumes string `yaml:"blockVolumes"`
	LoadBalancer string `yaml:"loadBalancer"`
	MeshSubnet   string `yaml:"meshSubnet"`
}

// Labels is the site's profile as Node labels, every key under Prefix. A
// capability the site does not have gets no label.
func (s Site) Labels() map[string]string {
	l := map[string]string{
		Prefix + KeyProvider:    s.Provider,
		Prefix + KeySite:        s.Name,
		Prefix + KeyReliability: s.Reliability,
		Prefix + KeyReach:       s.Reach,
	}
	if s.BlockVolumes != "" {
		l[Prefix+KeyBlockVolumes] = s.BlockVolumes
	}
	if s.LoadBalancer != "" {
		l[Prefix+KeyLoadBalancer] = s.LoadBalancer
	}
	return l
}

// Set is one environment's Node set file.
type Set struct {
	Path  string
	sites map[string]Site
	mesh  *Mesh
}

// Load reads and validates the Node set file at path.
func Load(path string) (*Set, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the Node set %s: %w", path, err)
	}
	return Parse(path, src)
}

// Parse reads and validates a Node set from src; path names it in errors.
func Parse(path string, src []byte) (*Set, error) {
	var f struct {
		Sites map[string]Site `yaml:"sites"`
		Mesh  *meshFile       `yaml:"mesh"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(src))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parsing the Node set %s: %w", path, err)
	}
	s := &Set{Path: path, sites: map[string]Site{}}
	for name, site := range f.Sites {
		site.Name = name
		s.sites[name] = site
	}
	mesh, meshErr := parseMesh(s.sites, f.Mesh)
	if err := errors.Join(s.validate(), meshErr); err != nil {
		return nil, fmt.Errorf("invalid Node set %s: %w", path, err)
	}
	s.mesh = mesh
	return s, nil
}

// Hcloud is the site of the Hetzner Cloud servers. A valid set has exactly
// one.
func (s *Set) Hcloud() Site {
	for _, site := range s.sites {
		if site.Provider == ProviderHcloud {
			return site
		}
	}
	return Site{}
}

func (s *Set) validate() error {
	var errs []error
	var hcloud []string
	for _, name := range slices.Sorted(maps.Keys(s.sites)) {
		site := s.sites[name]
		if !dnsLabel.MatchString(name) {
			errs = append(errs, fmt.Errorf("site %q: the name is not a DNS label (lower-case letters, digits and -, at most 63)", name))
		}
		errs = append(errs, kindName(name, "provider", site.Provider, true))
		errs = append(errs, oneOf(name, "reliability", site.Reliability, reliabilities))
		errs = append(errs, oneOf(name, "reach", site.Reach, reaches))
		errs = append(errs, kindName(name, "blockVolumes", site.BlockVolumes, false))
		errs = append(errs, kindName(name, "loadBalancer", site.LoadBalancer, false))
		if site.Provider == ProviderHcloud {
			hcloud = append(hcloud, name)
		}
	}
	if len(hcloud) != 1 {
		errs = append(errs, fmt.Errorf("exactly one site has provider %s, the site of the Hetzner servers; found %d %v",
			ProviderHcloud, len(hcloud), hcloud))
	}
	return errors.Join(errs...)
}

// kindName checks a provider or capability value.
func kindName(site, field, v string, required bool) error {
	if v == "" {
		if required {
			return fmt.Errorf("site %s: %s is required", site, field)
		}
		return nil
	}
	if !dnsLabel.MatchString(v) {
		return fmt.Errorf("site %s: %s %q is not a DNS label (lower-case letters, digits and -, at most 63)", site, field, v)
	}
	return nil
}

func oneOf(site, field, v string, allowed []string) error {
	if !slices.Contains(allowed, v) {
		return fmt.Errorf("site %s: %s %q must be one of %s", site, field, v, strings.Join(allowed, ", "))
	}
	return nil
}

// Change is what turns a Node's profile labels into a site's: the labels to
// set, and the keys under Prefix to remove.
type Change struct {
	Set    map[string]string
	Remove []string
}

// Diff compares a Node's labels with the profile it should carry. Only keys
// under Prefix count: every other label belongs to someone else.
func Diff(have, want map[string]string) Change {
	c := Change{Set: map[string]string{}}
	for k, v := range want {
		if got, ok := have[k]; !ok || got != v {
			c.Set[k] = v
		}
	}
	for k := range have {
		if _, ok := want[k]; !ok && strings.HasPrefix(k, Prefix) {
			c.Remove = append(c.Remove, k)
		}
	}
	slices.Sort(c.Remove)
	return c
}

// Empty reports whether the Node already carries the profile.
func (c Change) Empty() bool { return len(c.Set) == 0 && len(c.Remove) == 0 }

// String names the change with short keys: "set reach=public, site=hel1;
// remove load-balancer".
func (c Change) String() string {
	var parts []string
	if len(c.Set) > 0 {
		var set []string
		for _, k := range slices.Sorted(maps.Keys(c.Set)) {
			set = append(set, strings.TrimPrefix(k, Prefix)+"="+c.Set[k])
		}
		parts = append(parts, "set "+strings.Join(set, ", "))
	}
	if len(c.Remove) > 0 {
		var rm []string
		for _, k := range c.Remove {
			rm = append(rm, strings.TrimPrefix(k, Prefix))
		}
		parts = append(parts, "remove "+strings.Join(rm, ", "))
	}
	return strings.Join(parts, "; ")
}
