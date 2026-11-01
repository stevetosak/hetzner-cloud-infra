package coreset

import (
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"slices"
	"strings"
)

// checkProbes proves that every public host has a probe and every probe a
// host (ADR 0011): each hostname of an HTTPRoute the live environment syncs
// (the core list's builds and the httproute.yaml of each listed project, as
// the seed reads them) is the host of a static target of a Probe in a core
// build, and each such target's host is a hostname of one of those routes.
// A route without a probe would go down unseen; a probe without a route
// would page for a host nobody serves.
//
// A file that cannot be found or read stops the check. A route or Probe that
// cannot be decoded is named, and the check goes on with the others.
func checkProbes(fsys fs.FS, core []Element) []error {
	files, err := syncedRouteFiles(fsys, core)
	if err != nil {
		return []error{err}
	}
	idx, errs, err := indexHosts(fsys, files)
	if err != nil {
		return []error{err}
	}
	return append(errs, idx.drift()...)
}

// syncedRouteFiles returns the files that can hold a synced HTTPRoute or a
// Probe: every file of each core build, and the httproute.yaml of each
// listed project.
func syncedRouteFiles(fsys fs.FS, core []Element) ([]string, error) {
	var files []string
	for _, e := range core {
		built, err := buildFiles(fsys, e.Path)
		if err != nil {
			return nil, err
		}
		files = append(files, built...)
	}
	projects, err := Projects(fsys)
	if err != nil {
		return nil, err
	}
	for _, p := range projects {
		f := "projects/" + p + "/httproute.yaml"
		if _, err := fs.Stat(fsys, f); err == nil {
			files = append(files, f)
		}
	}
	return files, nil
}

// hostIndex maps each host to the file that names it.
type hostIndex struct {
	routes  map[string]string // hostname -> the file of its HTTPRoute
	targets map[string]string // host -> the file of its Probe
}

// indexHosts reads the routes' hostnames and the Probes' target hosts from
// files. errs names each object that cannot be decoded; err is a file that
// cannot be read.
func indexHosts(fsys fs.FS, files []string) (idx hostIndex, errs []error, err error) {
	idx = hostIndex{routes: map[string]string{}, targets: map[string]string{}}
	for _, f := range files {
		objs, err := Objects(fsys, f)
		if err != nil {
			return idx, nil, err
		}
		for _, o := range objs {
			var into map[string]string
			var hosts []string
			var err error
			switch o.Kind {
			case "HTTPRoute":
				into = idx.routes
				hosts, err = routeHostnames(o)
			case "Probe":
				into = idx.targets
				hosts, err = probeHosts(o)
			default:
				continue
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %s %s: %w", f, o.Kind, o.Metadata.Name, err))
				continue
			}
			for _, h := range hosts {
				into[h] = f
			}
		}
	}
	return idx, errs, nil
}

// drift names each route host without a probe and each probe target that no
// route serves.
func (idx hostIndex) drift() []error {
	var errs []error
	for _, h := range sortedKeys(idx.routes) {
		if _, ok := idx.targets[h]; !ok {
			errs = append(errs, fmt.Errorf("%s: host %s has no probe; add a target to a Probe (core/monitoring/probes/)", idx.routes[h], h))
		}
	}
	for _, h := range sortedKeys(idx.targets) {
		if _, ok := idx.routes[h]; !ok {
			errs = append(errs, fmt.Errorf("%s: a probe targets %s, which no synced HTTPRoute serves", idx.targets[h], h))
		}
	}
	return errs
}

// buildFiles returns the files of dir's kustomization, with the files of
// each base it lists. A missing or remote resource is checkKustomization's
// to refuse; it is skipped here.
func buildFiles(fsys fs.FS, dir string) ([]string, error) {
	resources, err := kustomizationResources(fsys, dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range resources {
		if strings.Contains(r, "://") {
			continue
		}
		rel := path.Join(dir, r)
		st, err := fs.Stat(fsys, rel)
		switch {
		case err != nil:
			continue
		case st.IsDir():
			base, err := buildFiles(fsys, rel)
			if err != nil {
				return nil, err
			}
			out = append(out, base...)
		default:
			out = append(out, rel)
		}
	}
	return out, nil
}

// routeHostnames returns an HTTPRoute's hostnames.
func routeHostnames(o Object) ([]string, error) {
	var r struct {
		Spec struct {
			Hostnames []string `yaml:"hostnames"`
		} `yaml:"spec"`
	}
	if err := o.Node.Decode(&r); err != nil {
		return nil, err
	}
	return r.Spec.Hostnames, nil
}

// probeHosts returns the host of each static target of a Probe. A Probe
// that discovers its targets (spec.targets.ingress) cannot be checked from
// files, so it is refused.
func probeHosts(o Object) ([]string, error) {
	var p struct {
		Spec struct {
			Targets struct {
				StaticConfig *struct {
					Static []string `yaml:"static"`
				} `yaml:"staticConfig"`
				Ingress any `yaml:"ingress"`
			} `yaml:"targets"`
		} `yaml:"spec"`
	}
	if err := o.Node.Decode(&p); err != nil {
		return nil, err
	}
	t := p.Spec.Targets
	if t.Ingress != nil || t.StaticConfig == nil {
		return nil, fmt.Errorf("list the targets in staticConfig.static, so the check can read them")
	}
	var out []string
	for _, s := range t.StaticConfig.Static {
		u, err := url.Parse(s)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			return nil, fmt.Errorf("target %q is not an https URL", s)
		}
		out = append(out, u.Hostname())
	}
	return out, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
