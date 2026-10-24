// Package coreset reads what ArgoCD syncs from this repository: the core
// ApplicationSet's list, its rehearsal cut, and the projects generator
// (ADR 0010, Amendment 2026-10-22). `kluster services check` proves the
// files agree with each other; the services seed reads the same lists.
//
// Every function takes the repository as an fs.FS rooted at its top, so a
// test can hand it a fstest.MapFS.
package coreset

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"gopkg.in/yaml.v3"
)

// The files this package reads, relative to the repository root.
const (
	CoreAppSetFile      = "core/argocd/core-applicationset.yaml"
	RehearsalArgoCDFile = "core/rehearsal/argocd/kustomization.yaml"
	ProjectsAppSetFile  = "core/argocd/applicationset.yaml"
	AppProjectsFile     = "core/argocd/appprojects.yaml"
	GatewayFile         = "core/gateway/gateway.yaml"
	ReloaderFile        = "core/reloader/reloader.yaml"
	AppsFile            = "deployments/apps.json"

	// RehearsalDir holds the rehearsal cuts; it is not a core directory.
	RehearsalDir = "core/rehearsal"

	// elementsPath is the JSON pointer the rehearsal cut replaces.
	elementsPath = "/spec/generators/0/list/elements"
)

// Element is one entry of the core ApplicationSet's list: the Application
// core-<Dir> syncs Path into Namespace.
type Element struct {
	Dir       string `yaml:"dir"`
	Path      string `yaml:"path"`
	Namespace string `yaml:"namespace"`
}

// Object is the part of a Kubernetes object every check reads. Node keeps
// the whole document for a kind-specific decode.
type Object struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name        string            `yaml:"name"`
		Namespace   string            `yaml:"namespace"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
	Node *yaml.Node `yaml:"-"`
}

// Objects parses every document of a YAML file. Empty documents are skipped.
func Objects(fsys fs.FS, name string) ([]Object, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var out []Object
	for {
		var n yaml.Node
		if err := dec.Decode(&n); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if len(n.Content) == 0 || n.Content[0].Kind != yaml.MappingNode {
			continue
		}
		var o Object
		if err := n.Decode(&o); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		o.Node = &n
		out = append(out, o)
	}
}

// find returns the one object of kind and name in a file.
func find(fsys fs.FS, file, kind, name string) (Object, error) {
	objs, err := Objects(fsys, file)
	if err != nil {
		return Object{}, err
	}
	for _, o := range objs {
		if o.Kind == kind && o.Metadata.Name == name {
			return o, nil
		}
	}
	return Object{}, fmt.Errorf("%s: no %s %q", file, kind, name)
}

// Core returns the core ApplicationSet's list.
func Core(fsys fs.FS) ([]Element, error) {
	o, err := find(fsys, CoreAppSetFile, "ApplicationSet", "core")
	if err != nil {
		return nil, err
	}
	var as struct {
		Spec struct {
			Generators []struct {
				List *struct {
					Elements []Element `yaml:"elements"`
				} `yaml:"list"`
			} `yaml:"generators"`
		} `yaml:"spec"`
	}
	if err := o.Node.Decode(&as); err != nil {
		return nil, fmt.Errorf("%s: %w", CoreAppSetFile, err)
	}
	if len(as.Spec.Generators) != 1 || as.Spec.Generators[0].List == nil {
		return nil, fmt.Errorf("%s: want exactly one list generator", CoreAppSetFile)
	}
	return checkElements(CoreAppSetFile, as.Spec.Generators[0].List.Elements)
}

// Rehearsal returns the list the rehearsal cut of core/argocd puts in place
// of the core list: the value of its JSON6902 replace on elementsPath.
func Rehearsal(fsys fs.FS) ([]Element, error) {
	data, err := fs.ReadFile(fsys, RehearsalArgoCDFile)
	if err != nil {
		return nil, err
	}
	var k struct {
		Patches []struct {
			Patch string `yaml:"patch"`
		} `yaml:"patches"`
	}
	if err := yaml.Unmarshal(data, &k); err != nil {
		return nil, fmt.Errorf("%s: %w", RehearsalArgoCDFile, err)
	}
	for _, p := range k.Patches {
		var ops []struct {
			Op    string    `yaml:"op"`
			Path  string    `yaml:"path"`
			Value []Element `yaml:"value"`
		}
		// A strategic-merge patch is a mapping, not a list of operations.
		if yaml.Unmarshal([]byte(p.Patch), &ops) != nil {
			continue
		}
		for _, op := range ops {
			if op.Op == "replace" && op.Path == elementsPath {
				return checkElements(RehearsalArgoCDFile, op.Value)
			}
		}
	}
	return nil, fmt.Errorf("%s: no replace of %s", RehearsalArgoCDFile, elementsPath)
}

func checkElements(file string, els []Element) ([]Element, error) {
	if len(els) == 0 {
		return nil, fmt.Errorf("%s: the list is empty", file)
	}
	seen := map[string]bool{}
	for _, e := range els {
		if e.Dir == "" || e.Path == "" || e.Namespace == "" {
			return nil, fmt.Errorf("%s: element %+v lacks dir, path or namespace", file, e)
		}
		if seen[e.Dir] {
			return nil, fmt.Errorf("%s: dir %s is listed twice", file, e.Dir)
		}
		seen[e.Dir] = true
	}
	return els, nil
}

// Projects returns the projects the projects ApplicationSet syncs, in the
// order its generator lists them. Each entry must have the shape
// projects/<project>/*/manifests/overlays/dev, which the template's
// naming rule needs.
func Projects(fsys fs.FS) ([]string, error) {
	o, err := find(fsys, ProjectsAppSetFile, "ApplicationSet", "projects")
	if err != nil {
		return nil, err
	}
	var as struct {
		Spec struct {
			Generators []struct {
				Git *struct {
					Directories []struct {
						Path    string `yaml:"path"`
						Exclude bool   `yaml:"exclude"`
					} `yaml:"directories"`
				} `yaml:"git"`
			} `yaml:"generators"`
		} `yaml:"spec"`
	}
	if err := o.Node.Decode(&as); err != nil {
		return nil, fmt.Errorf("%s: %w", ProjectsAppSetFile, err)
	}
	if len(as.Spec.Generators) != 1 || as.Spec.Generators[0].Git == nil {
		return nil, fmt.Errorf("%s: want exactly one git generator", ProjectsAppSetFile)
	}
	var out []string
	for _, d := range as.Spec.Generators[0].Git.Directories {
		if d.Exclude {
			continue
		}
		seg := strings.Split(d.Path, "/")
		if len(seg) != 6 || seg[0] != "projects" || seg[1] == "" || strings.ContainsAny(seg[1], "*?[") ||
			seg[2] != "*" || strings.Join(seg[3:], "/") != OverlaySuffix {
			return nil, fmt.Errorf("%s: %q is not projects/<project>/*/%s", ProjectsAppSetFile, d.Path, OverlaySuffix)
		}
		out = append(out, seg[1])
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: the generator lists no project", ProjectsAppSetFile)
	}
	return out, nil
}

// OverlaySuffix is the part of a component's synced path after its name.
const OverlaySuffix = "manifests/overlays/dev"

// Components returns the components of a project the generator turns into
// Applications: each directory with an overlay at the synced path.
func Components(fsys fs.FS, project string) ([]string, error) {
	dirs, err := fs.Glob(fsys, "projects/"+project+"/*/"+OverlaySuffix)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range dirs {
		if st, err := fs.Stat(fsys, d); err != nil || !st.IsDir() {
			continue
		}
		out = append(out, strings.Split(d, "/")[2])
	}
	return out, nil
}
