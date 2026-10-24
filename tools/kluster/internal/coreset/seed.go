package coreset

import (
	"fmt"
	"io/fs"
	"path"
	"slices"
)

// Seed is what the services seed reads for one environment: the core list
// that environment syncs, the projects it syncs, and the files the seed
// applies itself because ArgoCD cannot or must not own them (ADR 0010,
// Amendment 2026-10-22).
type Seed struct {
	Rehearsal bool
	Elements  []Element
	// Projects is empty in a rehearsal: its cut removes the projects
	// ApplicationSet.
	Projects []string
	// CRDFiles are every core/<dir>/crds.yaml, listed directory or not:
	// core/gateway-api holds CRDs only and syncs nothing.
	CRDFiles []string
	// NamespaceFiles are the namespace files of each listed directory and,
	// on live, of each project.
	NamespaceFiles []string
}

// namespaceFiles are the names a directory may give its Namespaces.
var namespaceFiles = []string{"namespace.yaml", "namespaces.yaml"}

// SeedFor reads the seed's view of one environment.
func SeedFor(fsys fs.FS, rehearsal bool) (*Seed, error) {
	s := &Seed{Rehearsal: rehearsal}
	var err error
	if rehearsal {
		s.Elements, err = Rehearsal(fsys)
	} else {
		s.Elements, err = Core(fsys)
	}
	if err != nil {
		return nil, err
	}
	if !rehearsal {
		if s.Projects, err = Projects(fsys); err != nil {
			return nil, err
		}
	}
	if s.CRDFiles, err = fs.Glob(fsys, "core/*/crds.yaml"); err != nil {
		return nil, err
	}
	dirs := make([]string, 0, len(s.Elements)+len(s.Projects))
	for _, e := range s.Elements {
		// A rehearsal cut builds on core/<dir>, which holds the namespace file.
		dirs = append(dirs, "core/"+e.Dir)
	}
	for _, p := range s.Projects {
		dirs = append(dirs, "projects/"+p)
	}
	for _, d := range dirs {
		for _, n := range namespaceFiles {
			f := path.Join(d, n)
			if _, err := fs.Stat(fsys, f); err == nil {
				s.NamespaceFiles = append(s.NamespaceFiles, f)
			}
		}
	}
	return s, nil
}

// Namespaces returns the names of the Namespaces in NamespaceFiles, sorted.
// Every element's namespace must be one of them: nothing else makes it.
func (s *Seed) Namespaces(fsys fs.FS) ([]string, error) {
	var out []string
	for _, f := range s.NamespaceFiles {
		objs, err := Objects(fsys, f)
		if err != nil {
			return nil, err
		}
		for _, o := range objs {
			if o.Kind != "Namespace" {
				return nil, fmt.Errorf("%s: holds %s %s; a namespace file holds Namespaces only", f, o.Kind, o.Metadata.Name)
			}
			out = append(out, o.Metadata.Name)
		}
	}
	slices.Sort(out)
	out = slices.Compact(out)
	for _, e := range s.Elements {
		if !slices.Contains(out, e.Namespace) {
			return nil, fmt.Errorf("namespace %s of core-%s is in no namespace file of core/%s", e.Namespace, e.Dir, e.Dir)
		}
	}
	return out, nil
}

// Applications returns the names of the Applications the core list makes.
func (s *Seed) Applications() []string {
	out := make([]string, len(s.Elements))
	for i, e := range s.Elements {
		out[i] = "core-" + e.Dir
	}
	return out
}
