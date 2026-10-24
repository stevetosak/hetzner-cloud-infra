package coreset

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
)

// reloaderAnnotation marks a Deployment Reloader restarts; any key in its
// group opts in (`secret.reloader.stakater.com/auto`, `…/reload`, …).
const reloaderAnnotation = "reloader.stakater.com/"

// Check proves the synced files agree with each other. It reads files only:
// no cluster, no key. Each error names a file and what is missing.
func Check(fsys fs.FS) []error {
	var errs []error
	add := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	core, err := Core(fsys)
	if err != nil {
		return []error{err}
	}
	dests, err := destinations(fsys, "core")
	if err != nil {
		return []error{err}
	}

	for _, e := range core {
		if e.Path != "core/"+e.Dir {
			add(fmt.Errorf("%s: dir %s syncs %s; the main list syncs core/<dir>", CoreAppSetFile, e.Dir, e.Path))
		}
		if !slices.Contains(dests, e.Namespace) {
			add(fmt.Errorf("%s: namespace %s of %s is not a destination of the core AppProject", AppProjectsFile, e.Namespace, e.Dir))
		}
		errs = append(errs, checkKustomization(fsys, e.Path)...)
	}
	errs = append(errs, orphans(fsys, "core", core, func(e Element) string { return e.Path })...)
	errs = append(errs, checkRehearsal(fsys, core)...)
	errs = append(errs, checkProjects(fsys, dests)...)
	for _, rehearsal := range []bool{false, true} {
		s, err := SeedFor(fsys, rehearsal)
		if err == nil {
			_, err = s.Namespaces(fsys)
		}
		add(err)
		_, err = TargetRevision(fsys, rehearsal)
		add(err)
	}
	errs = append(errs, checkRehearsalSecrets(fsys)...)
	return errs
}

// FreshSecrets returns the Secrets a rehearsal makes fresh: each one in a
// rehearsal namespace that is not marked rehearsal: omit.
func FreshSecrets(cat *catalog.Catalog, namespaces []string) []*catalog.Secret {
	var out []*catalog.Secret
	for _, s := range cat.Secrets {
		if slices.Contains(namespaces, s.Ref.Namespace) && !s.OmittedInRehearsal() {
			out = append(out, s)
		}
	}
	return out
}

// checkRehearsalSecrets proves a rehearsal can make each of its Secrets
// fresh in memory (ADR 0009, Amendment 2026-10-22): no issued key, because
// only a live value exists for it, and no copy of a Secret the rehearsal does
// not make. A rehearsal: omit on a Secret no rehearsal makes is stale.
func checkRehearsalSecrets(fsys fs.FS) []error {
	data, err := fs.ReadFile(fsys, catalog.FileName)
	if err != nil {
		return []error{err}
	}
	cat, err := catalog.Parse(data, ".")
	if err != nil {
		return []error{fmt.Errorf("%s: %w", catalog.FileName, err)}
	}
	s, err := SeedFor(fsys, true)
	if err != nil {
		return []error{err}
	}
	nss, err := s.Namespaces(fsys)
	if err != nil {
		return []error{err}
	}
	fresh := FreshSecrets(cat, nss)
	made := map[catalog.Ref]bool{}
	for _, f := range fresh {
		made[f.Ref] = true
	}
	var errs []error
	for _, f := range fresh {
		for _, name := range f.KeyNames() {
			k := f.Keys[name]
			switch {
			case k.Kind == catalog.Issued:
				errs = append(errs, fmt.Errorf("%s: %s.%s is issued and a rehearsal makes the Secret fresh: set rehearsal: %s, with the reason in its note",
					catalog.FileName, f.Ref, name, catalog.RehearsalOmit))
			case k.Kind == catalog.Copy && !made[k.Source().Secret]:
				errs = append(errs, fmt.Errorf("%s: %s.%s copies %s, which a rehearsal does not make", catalog.FileName, f.Ref, name, k.Source()))
			}
		}
	}
	for _, sec := range cat.Secrets {
		if sec.OmittedInRehearsal() && !slices.Contains(nss, sec.Ref.Namespace) {
			errs = append(errs, fmt.Errorf("%s: %s has rehearsal: %s, but no rehearsal namespace holds it", catalog.FileName, sec.Ref, catalog.RehearsalOmit))
		}
	}
	return errs
}

// checkRehearsal refuses a rehearsal element the main list does not have,
// or one that points anywhere but the main directory or its own cut.
func checkRehearsal(fsys fs.FS, core []Element) []error {
	reh, err := Rehearsal(fsys)
	if err != nil {
		return []error{err}
	}
	main := map[string]Element{}
	for _, e := range core {
		main[e.Dir] = e
	}
	var errs []error
	for _, e := range reh {
		m, ok := main[e.Dir]
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("%s: dir %s is not in the main list in %s", RehearsalArgoCDFile, e.Dir, CoreAppSetFile))
			continue
		case e.Namespace != m.Namespace:
			errs = append(errs, fmt.Errorf("%s: dir %s has namespace %s, the main list %s", RehearsalArgoCDFile, e.Dir, e.Namespace, m.Namespace))
		case e.Path != m.Path && e.Path != RehearsalDir+"/"+e.Dir:
			errs = append(errs, fmt.Errorf("%s: dir %s syncs %s; want %s or %s/%s", RehearsalArgoCDFile, e.Dir, e.Path, m.Path, RehearsalDir, e.Dir))
		}
		if e.Path != m.Path {
			errs = append(errs, checkKustomization(fsys, e.Path)...)
		}
	}
	return append(errs, orphans(fsys, RehearsalDir, reh, func(e Element) string { return e.Path })...)
}

// orphans refuses a kustomization directly under parent that no element
// syncs: it would look synced and never be.
func orphans(fsys fs.FS, parent string, els []Element, pathOf func(Element) string) []error {
	found, err := fs.Glob(fsys, parent+"/*/kustomization.yaml")
	if err != nil {
		return []error{err}
	}
	var errs []error
	for _, f := range found {
		dir := path.Dir(f)
		if dir == RehearsalDir {
			continue
		}
		if !slices.ContainsFunc(els, func(e Element) bool { return pathOf(e) == dir }) {
			errs = append(errs, fmt.Errorf("%s: no Application syncs it; list it or delete it", f))
		}
	}
	return errs
}

// checkKustomization refuses what only the seed or kluster may apply, a
// missing file, and a remote resource nobody reviewed.
func checkKustomization(fsys fs.FS, dir string) []error {
	file := dir + "/kustomization.yaml"
	objs, err := Objects(fsys, file)
	if err != nil {
		return []error{err}
	}
	if len(objs) != 1 {
		return []error{fmt.Errorf("%s: want one document", file)}
	}
	var k struct {
		Resources []string `yaml:"resources"`
	}
	if err := objs[0].Node.Decode(&k); err != nil {
		return []error{fmt.Errorf("%s: %w", file, err)}
	}
	var errs []error
	for _, r := range k.Resources {
		if strings.Contains(r, "://") {
			errs = append(errs, fmt.Errorf("%s: remote resource %s; render it into the repository", file, r))
			continue
		}
		rel := path.Join(dir, r)
		if why := forbidden(rel); why != "" {
			errs = append(errs, fmt.Errorf("%s: lists %s, %s", file, r, why))
			continue
		}
		st, err := fs.Stat(fsys, rel)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: lists %s, which does not exist", file, r))
			continue
		}
		if st.IsDir() {
			// A base: its own kustomization is checked where it is listed.
			continue
		}
		errs = append(errs, checkContent(fsys, file, rel)...)
	}
	return errs
}

// forbidden says why a resource path must never be in a synced build, or "".
func forbidden(rel string) string {
	base := path.Base(rel)
	switch {
	case strings.HasSuffix(base, ".enc.yaml"):
		return "a Secret: kluster secrets apply owns it"
	case base == "pg-cluster.yaml" || slices.Contains(strings.Split(rel, "/"), "recovery"):
		return "the database Cluster: kluster db create owns it, a sync must never change it"
	case base == "crds.yaml":
		return "CRDs: the seed owns them"
	case base == "namespace.yaml" || base == "namespaces.yaml":
		return "Namespaces: the seed owns them"
	case strings.Contains(base, "values") && strings.HasSuffix(base, ".yaml"):
		return "Helm values, not manifests"
	}
	return ""
}

// checkContent refuses a CRD or a Namespace whatever the file is called.
func checkContent(fsys fs.FS, file, rel string) []error {
	objs, err := Objects(fsys, rel)
	if err != nil {
		return []error{err}
	}
	var errs []error
	for _, o := range objs {
		if o.Kind == "CustomResourceDefinition" || o.Kind == "Namespace" {
			errs = append(errs, fmt.Errorf("%s: %s holds %s %s; the seed owns that kind", file, rel, o.Kind, o.Metadata.Name))
		}
	}
	return errs
}

// destinations returns the namespaces an AppProject may write to.
func destinations(fsys fs.FS, project string) ([]string, error) {
	o, err := find(fsys, AppProjectsFile, "AppProject", project)
	if err != nil {
		return nil, err
	}
	var p struct {
		Spec struct {
			Destinations []struct {
				Namespace string `yaml:"namespace"`
			} `yaml:"destinations"`
		} `yaml:"spec"`
	}
	if err := o.Node.Decode(&p); err != nil {
		return nil, fmt.Errorf("%s: %w", AppProjectsFile, err)
	}
	var out []string
	for _, d := range p.Spec.Destinations {
		out = append(out, d.Namespace)
	}
	return out, nil
}

// checkProjects treats the projects generator as the source of truth: each
// project it lists needs the other places a project is named.
func checkProjects(fsys fs.FS, coreDests []string) []error {
	projects, err := Projects(fsys)
	if err != nil {
		return []error{err}
	}
	listeners, err := gatewayNamespaces(fsys)
	if err != nil {
		return []error{err}
	}
	apps, err := appNames(fsys)
	if err != nil {
		return []error{err}
	}
	reloader, err := reloaderScope(fsys)
	if err != nil {
		return []error{err}
	}

	var errs []error
	for _, p := range projects {
		if dests, err := destinations(fsys, p); err != nil {
			errs = append(errs, err)
		} else if !slices.Equal(dests, []string{p}) {
			errs = append(errs, fmt.Errorf("%s: AppProject %s has destinations %v; want only %s", AppProjectsFile, p, dests, p))
		}
		if _, err := find(fsys, "projects/"+p+"/namespace.yaml", "Namespace", p); err != nil {
			errs = append(errs, err)
		}
		for name, nss := range listeners {
			if !slices.Contains(nss, p) {
				errs = append(errs, fmt.Errorf("%s: listener %s does not admit routes from %s", GatewayFile, name, p))
			}
		}
		comps, err := Components(fsys, p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, c := range comps {
			if !apps[p+"-"+c] {
				errs = append(errs, fmt.Errorf("%s: no entry %s-%s (the generated Application name)", AppsFile, p, c))
			}
		}
		reloaded, err := hasReloadedDeployment(fsys, p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if reloaded {
			errs = append(errs, reloader.covers(p)...)
			if !slices.Contains(coreDests, p) {
				errs = append(errs, fmt.Errorf("%s: %s has a Deployment Reloader restarts, but is not a destination of the core AppProject", AppProjectsFile, p))
			}
		}
	}
	return errs
}

// gatewayNamespaces returns, per listener of the Gateway tosak, the
// namespaces its allowedRoutes selector admits.
func gatewayNamespaces(fsys fs.FS) (map[string][]string, error) {
	o, err := find(fsys, GatewayFile, "Gateway", "tosak")
	if err != nil {
		return nil, err
	}
	var gw struct {
		Spec struct {
			Listeners []struct {
				Name          string `yaml:"name"`
				AllowedRoutes struct {
					Namespaces struct {
						From     string `yaml:"from"`
						Selector struct {
							MatchExpressions []struct {
								Key      string   `yaml:"key"`
								Operator string   `yaml:"operator"`
								Values   []string `yaml:"values"`
							} `yaml:"matchExpressions"`
						} `yaml:"selector"`
					} `yaml:"namespaces"`
				} `yaml:"allowedRoutes"`
			} `yaml:"listeners"`
		} `yaml:"spec"`
	}
	if err := o.Node.Decode(&gw); err != nil {
		return nil, fmt.Errorf("%s: %w", GatewayFile, err)
	}
	out := map[string][]string{}
	for _, l := range gw.Spec.Listeners {
		var nss []string
		for _, m := range l.AllowedRoutes.Namespaces.Selector.MatchExpressions {
			if m.Key == "kubernetes.io/metadata.name" && m.Operator == "In" {
				nss = append(nss, m.Values...)
			}
		}
		out[l.Name] = nss
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: the Gateway has no listener", GatewayFile)
	}
	return out, nil
}

// appNames returns the keys of the deploy catalog's app map.
func appNames(fsys fs.FS) (map[string]bool, error) {
	data, err := fs.ReadFile(fsys, AppsFile)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", AppsFile, err)
	}
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out, nil
}

// hasReloadedDeployment reports whether a Deployment in a project's bases
// carries a Reloader annotation.
func hasReloadedDeployment(fsys fs.FS, project string) (bool, error) {
	files, err := fs.Glob(fsys, "projects/"+project+"/*/manifests/base/*.yaml")
	if err != nil {
		return false, err
	}
	for _, f := range files {
		objs, err := Objects(fsys, f)
		if err != nil {
			return false, err
		}
		for _, o := range objs {
			if o.Kind != "Deployment" {
				continue
			}
			for k := range o.Metadata.Annotations {
				if strings.Contains(k, reloaderAnnotation) {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// scope is what Reloader is allowed to watch in scoped mode: the
// --namespaces argument and the namespaces that hold its Role and
// RoleBinding.
type scope struct {
	args, roles, bindings []string
}

func reloaderScope(fsys fs.FS) (scope, error) {
	objs, err := Objects(fsys, ReloaderFile)
	if err != nil {
		return scope{}, err
	}
	var s scope
	for _, o := range objs {
		switch o.Kind {
		case "Role":
			s.roles = append(s.roles, o.Metadata.Namespace)
		case "RoleBinding":
			s.bindings = append(s.bindings, o.Metadata.Namespace)
		case "Deployment":
			var d struct {
				Spec struct {
					Template struct {
						Spec struct {
							Containers []struct {
								Args []string `yaml:"args"`
							} `yaml:"containers"`
						} `yaml:"spec"`
					} `yaml:"template"`
				} `yaml:"spec"`
			}
			if err := o.Node.Decode(&d); err != nil {
				return scope{}, fmt.Errorf("%s: %w", ReloaderFile, err)
			}
			for _, c := range d.Spec.Template.Spec.Containers {
				for _, a := range c.Args {
					if v, ok := strings.CutPrefix(a, "--namespaces="); ok {
						s.args = append(s.args, strings.Split(v, ",")...)
					}
				}
			}
		}
	}
	if len(s.args) == 0 {
		return scope{}, fmt.Errorf("%s: no --namespaces argument; Reloader is not in scoped mode", ReloaderFile)
	}
	return s, nil
}

func (s scope) covers(ns string) []error {
	var errs []error
	if !slices.Contains(s.args, ns) {
		errs = append(errs, fmt.Errorf("%s: --namespaces does not name %s, which has a Deployment Reloader restarts", ReloaderFile, ns))
	}
	if !slices.Contains(s.roles, ns) || !slices.Contains(s.bindings, ns) {
		errs = append(errs, fmt.Errorf("%s: no Role and RoleBinding in %s; render it with %s in the values", ReloaderFile, ns, ns))
	}
	return errs
}
