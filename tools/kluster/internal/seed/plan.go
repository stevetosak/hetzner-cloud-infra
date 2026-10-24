package seed

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/coreset"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kbuild"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/secrets"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// The Gateway whose load balancer the zone points at.
const (
	GatewayName      = "tosak"
	GatewayNamespace = "gateway"
)

// Inputs is what one seed is built from.
type Inputs struct {
	Rehearsal bool
	// WorkTree is the repository's top. The CRDs, the Namespaces and the
	// Secrets come from it: ArgoCD never reads them, so a CRD upgrade is
	// seeded from the branch and then merged.
	WorkTree string
	// Synced is an export of the revision the environment's ArgoCD reads.
	// What ArgoCD owns is built from it, never from the working tree, or a
	// seed and the next self-heal would fight.
	Synced  string
	Catalog *catalog.Catalog
	// Store reads the Secret files; live only.
	Store   *secrets.Store
	Zone    ZoneReader
	Timeout time.Duration
	Poll    time.Duration
}

// Element is one core Application's build, for the Plan Mode diff.
type Element struct {
	App      string
	Manifest []byte
	Target   Target
}

// Plan is one seed, built before the Control Plane is reached.
type Plan struct {
	Stages   []stage.Stage
	Elements []Element
	Hosts    []string
}

// Build reads both trees and builds every manifest the seed applies.
func Build(in Inputs) (*Plan, error) {
	wt := os.DirFS(in.WorkTree)
	ws, err := coreset.SeedFor(wt, in.Rehearsal)
	if err != nil {
		return nil, err
	}
	namespaces, err := ws.Namespaces(wt)
	if err != nil {
		return nil, err
	}
	crds, err := concat(wt, ws.CRDFiles)
	if err != nil {
		return nil, err
	}
	nsFiles, err := concat(wt, ws.NamespaceFiles)
	if err != nil {
		return nil, err
	}

	synced := os.DirFS(in.Synced)
	ss, err := coreset.SeedFor(synced, in.Rehearsal)
	if err != nil {
		return nil, fmt.Errorf("the synced revision: %w", err)
	}
	p := &Plan{}
	var argocd *Element
	for _, e := range ss.Elements {
		if !slices.Contains(namespaces, e.Namespace) {
			return nil, fmt.Errorf("the synced revision syncs core-%s into %s, which the working tree makes no Namespace for", e.Dir, e.Namespace)
		}
		app := "core-" + e.Dir
		m, err := kbuild.YAML(filepath.Join(in.Synced, filepath.FromSlash(e.Path)), app, e.Namespace)
		if err != nil {
			return nil, err
		}
		p.Elements = append(p.Elements, Element{App: app, Manifest: m, Target: Target{Manager: ManagerArgoCD}})
		if e.Dir == "argocd" {
			argocd = &p.Elements[len(p.Elements)-1]
		}
	}
	if argocd == nil {
		return nil, errors.New("the synced revision's core list has no argocd: nothing would keep ArgoCD in step")
	}
	routes := [][]byte{}
	for _, e := range p.Elements {
		routes = append(routes, e.Manifest)
	}
	for _, proj := range ss.Projects {
		b, err := fs.ReadFile(synced, "projects/"+proj+"/httproute.yaml")
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		routes = append(routes, b)
	}
	if p.Hosts, err = hostnames(routes); err != nil {
		return nil, err
	}

	var made []*catalog.Secret
	if in.Rehearsal {
		made = coreset.FreshSecrets(in.Catalog, namespaces)
	} else {
		for _, s := range in.Catalog.Secrets {
			if slices.Contains(namespaces, s.Ref.Namespace) {
				made = append(made, s)
			}
		}
	}
	addr := new(string)
	wait := Waiting{Timeout: in.Timeout, Poll: in.Poll}
	p.Stages = []stage.Stage{
		&Apply{StageName: "seed-crds", Manifest: crds, Target: Target{Manager: ManagerSeed},
			What: fmt.Sprintf("the CRDs of %d file(s)", len(ws.CRDFiles))},
		&Apply{StageName: "seed-namespaces", Manifest: nsFiles, Target: Target{Manager: ManagerSeed},
			What: fmt.Sprintf("%d Namespace(s)", len(namespaces))},
		&Secrets{Rehearsal: in.Rehearsal, Secrets: made, Store: in.Store},
		&Apply{StageName: "seed-argocd", Manifest: argocd.Manifest, Target: argocd.Target,
			What: "the build of core-argocd at the synced revision"},
		&Healthy{Applications: ss.Applications(), Waiting: wait},
		&Address{Gateway: GatewayName, Namespace: GatewayNamespace, Waiting: wait, Got: addr},
		&Zone{Reader: in.Zone, Hosts: p.Hosts, Address: addr},
	}
	return p, nil
}

// concat joins files into one multi-document manifest.
func concat(fsys fs.FS, files []string) ([]byte, error) {
	var b bytes.Buffer
	for _, f := range files {
		data, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, err
		}
		b.WriteString("---\n")
		b.Write(data)
		if !bytes.HasSuffix(data, []byte("\n")) {
			b.WriteByte('\n')
		}
	}
	return b.Bytes(), nil
}

// hostnames returns every hostname of the HTTPRoutes in docs, sorted.
func hostnames(docs [][]byte) ([]string, error) {
	var out []string
	for _, d := range docs {
		dec := yaml.NewDecoder(bytes.NewReader(d))
		for {
			var o struct {
				Kind string `yaml:"kind"`
				Spec struct {
					Hostnames []string `yaml:"hostnames"`
				} `yaml:"spec"`
			}
			if err := dec.Decode(&o); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return nil, err
			}
			if o.Kind == "HTTPRoute" {
				out = append(out, o.Spec.Hostnames...)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// Diffs prints, for each core Application, what its first sync would change
// (Plan Mode; information only: ArgoCD applies these, not the seed). With
// dir set, each full diff is written there as <app>.diff.
func (p *Plan) Diffs(ctx context.Context, h *stage.Host, out io.Writer, dir string) error {
	for _, e := range p.Elements {
		d, err := diff(ctx, h, e.App, e.Manifest, e.Target)
		if err != nil {
			// A kind whose CRD is not on the cluster yet cannot be diffed.
			fmt.Fprintf(out, "[%s] %s: not diffed: %v\n", h.Name, e.App, err)
			continue
		}
		fmt.Fprintf(out, "[%s] %s: %s", h.Name, e.App, d.Summary())
		if dir != "" && !d.Empty() {
			if err := os.WriteFile(filepath.Join(dir, e.App+".diff"), []byte(d.Text), 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}
