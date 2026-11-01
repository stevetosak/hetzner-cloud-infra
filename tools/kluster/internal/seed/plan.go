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
	Store *secrets.Store
	// Vault reads the rehearsal items of issued keys; a rehearsal only.
	Vault secrets.Vault
	Zone  ZoneReader
	// RehearsalBucket is the rehearsal archive bucket, which the
	// rehearsal-cuts Stage expects the archive ObjectStore to name.
	RehearsalBucket string
	Timeout         time.Duration
	Poll            time.Duration
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
	// Zone is the last of Stages. kluster up runs it after the database,
	// at the very end.
	Zone *Zone
	// Monitoring is the health test of a rehearsal (nil on live). It is not
	// in Stages: kluster up runs it after the database, before the zone,
	// because the CNPG rules fire while no database Cluster exists.
	Monitoring *Monitoring
}

// AfterDB is what kluster up runs once the database is built: the
// monitoring test in a rehearsal, then the zone.
func (p *Plan) AfterDB() []stage.Stage {
	if p.Monitoring != nil {
		return []stage.Stage{p.Monitoring, p.Zone}
	}
	return []stage.Stage{p.Zone}
}

// BeforeZone is every Stage but the zone.
func (p *Plan) BeforeZone() []stage.Stage {
	return slices.DeleteFunc(slices.Clone(p.Stages), func(s stage.Stage) bool { return s == stage.Stage(p.Zone) })
}

// Build reads both trees and builds every manifest the seed applies.
func Build(in Inputs) (*Plan, error) {
	if in.Rehearsal && in.RehearsalBucket == "" {
		return nil, errors.New("a rehearsal seed needs the rehearsal archive bucket, for the rehearsal-cuts Stage")
	}
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
		if err := inNamespaces(app, m, namespaces); err != nil {
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
		&Secrets{Rehearsal: in.Rehearsal, Secrets: made, Store: in.Store, Vault: in.Vault},
		&Apply{StageName: "seed-argocd", Manifest: argocd.Manifest, Target: argocd.Target,
			What: "the build of core-argocd at the synced revision"},
		&Healthy{Applications: ss.Applications(), Waiting: wait},
	}
	// The cuts are proven once core is in step, before anything goes on
	// (ADR 0009, Amendment 2026-10-25).
	if in.Rehearsal {
		p.Stages = append(p.Stages, &Cuts{Bucket: in.RehearsalBucket})
		p.Monitoring = &Monitoring{Waiting: wait}
	}
	p.Zone = &Zone{Reader: in.Zone, Hosts: p.Hosts, Address: addr, Rehearsal: in.Rehearsal}
	p.Stages = append(p.Stages,
		&Address{Gateway: GatewayName, Namespace: GatewayNamespace, Waiting: wait, Got: addr},
		p.Zone)
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

// inNamespaces refuses a build that puts an object into a namespace the
// environment makes no Namespace for: ArgoCD would retry its sync for ever. A
// rehearsal syncs no project, so a core object in a project's namespace needs
// a rehearsal cut (core/rehearsal/reloader is one).
func inNamespaces(app string, manifest []byte, namespaces []string) error {
	dec := yaml.NewDecoder(bytes.NewReader(manifest))
	for {
		var o struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
		}
		if err := dec.Decode(&o); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if ns := o.Metadata.Namespace; ns != "" && !slices.Contains(namespaces, ns) {
			return fmt.Errorf("%s puts %s %s/%s into a namespace this environment makes no Namespace for", app, o.Kind, ns, o.Metadata.Name)
		}
	}
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

// Readable reads, in memory, every value the seed-secrets Stage will need,
// and discards it: live decrypts each Secret's file with the age key, a
// rehearsal makes its fresh Secrets once, which reads every rehearsal item.
// So a front check (kluster up) refuses an unreadable Bitwarden item before
// anything is built. Nothing is printed or kept.
func (p *Plan) Readable(ctx context.Context) []error {
	var s *Secrets
	for _, st := range p.Stages {
		if x, ok := st.(*Secrets); ok {
			s = x
		}
	}
	if s == nil {
		return []error{errors.New("the seed has no seed-secrets Stage")}
	}
	if s.Rehearsal {
		if _, err := secrets.Fresh(ctx, s.Secrets, s.Vault); err != nil {
			return []error{fmt.Errorf("the rehearsal Secrets: %w", err)}
		}
		return nil
	}
	var errs []error
	for _, sec := range s.Secrets {
		if _, found, _, err := s.Store.Read(ctx, sec); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", sec.Ref, err))
		} else if !found {
			errs = append(errs, fmt.Errorf("%s: its file %s does not exist", sec.Ref, sec.File))
		}
	}
	return errs
}
