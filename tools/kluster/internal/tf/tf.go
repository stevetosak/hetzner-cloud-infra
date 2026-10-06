// Package tf runs one Terraform root Module the way ADR 0004 requires: plan
// to a saved file, read the plan JSON, check it against the command's Intent,
// and apply that saved plan and nothing else.
package tf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"

	"github.com/hashicorp/terraform-exec/tfexec"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/env"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/intent"
)

// ErrIntent is returned when an --apply run's plan does not match its Intent.
var ErrIntent = errors.New("plan does not match the intent")

// RunDir is a private directory for one kluster run. It holds the var files
// and saved plans, which carry the Hetzner token and seeded host keys in
// plain text, and is removed when the run ends. It prefers XDG_RUNTIME_DIR,
// a per-user tmpfs, so they never reach a disk.
type RunDir struct {
	path string
	n    int
}

// NewRunDir creates the run directory, readable by its owner only.
func NewRunDir() (*RunDir, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	p, err := os.MkdirTemp(base, "kluster-run-*")
	if err != nil {
		return nil, fmt.Errorf("creating run directory: %w", err)
	}
	if err := os.Chmod(p, 0o700); err != nil {
		return nil, err
	}
	return &RunDir{path: p}, nil
}

// Path is the run directory, for other files that must not reach a disk.
func (r *RunDir) Path() string { return r.path }

// Close removes the run directory and everything in it.
func (r *RunDir) Close() error { return os.RemoveAll(r.path) }

func (r *RunDir) next(prefix, ext string) string {
	r.n++
	return filepath.Join(r.path, fmt.Sprintf("%s-%d%s", prefix, r.n, ext))
}

// Module is one Terraform root Module, initialised for one environment.
type Module struct {
	Name string
	Dir  string
	env  *env.Environment
	run  *RunDir
	tf   *tfexec.Terraform
	// varFiles are passed to every plan, in order, before the run's own
	// var file, so the run's values win over theirs.
	varFiles []string
}

// Open initialises the Module in dir for the environment e. Every plan
// passes varFiles with -var-file.
func Open(ctx context.Context, name, dir string, e *env.Environment, run *RunDir, varFiles ...string) (*Module, error) {
	bin, err := exec.LookPath("terraform")
	if err != nil {
		return nil, fmt.Errorf("terraform not found on PATH: %w", err)
	}
	t, err := tfexec.NewTerraform(dir, bin)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if err := t.SetEnv(e.TerraformEnv(os.Environ(), dir)); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	var opts []tfexec.InitOption
	for _, b := range e.BackendConfig() {
		opts = append(opts, tfexec.BackendConfig(b))
	}
	if err := t.Init(ctx, opts...); err != nil {
		return nil, fmt.Errorf("terraform init %s (env %s): %w", name, e.Name, err)
	}
	return &Module{Name: name, Dir: dir, env: e, run: run, tf: t, varFiles: varFiles}, nil
}

// Plan is a saved plan and the changes it holds.
type Plan struct {
	Module  string
	Env     string
	File    string
	Changes []intent.Change
}

// Plan plans the Module with vars and saves the plan in the run directory.
// The Hetzner token is added to vars here, so no caller handles it.
func (m *Module) Plan(ctx context.Context, vars map[string]any) (*Plan, error) {
	return m.PlanReplace(ctx, vars)
}

// PlanReplace is Plan with -replace for each address: Terraform plans each
// of those resources to be destroyed and created again.
func (m *Module) PlanReplace(ctx context.Context, vars map[string]any, replace ...string) (*Plan, error) {
	all := map[string]any{"HCLOUD_TOKEN": m.env.HcloudToken()}
	for k, v := range vars {
		all[k] = v
	}
	varFile := m.run.next(m.Name, ".tfvars.json")
	data, err := json.Marshal(all)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(varFile, data, 0o600); err != nil {
		return nil, err
	}

	planFile := m.run.next(m.Name, ".tfplan")
	var opts []tfexec.PlanOption
	for _, f := range append(slices.Clone(m.varFiles), varFile) {
		opts = append(opts, tfexec.VarFile(f))
	}
	for _, r := range replace {
		opts = append(opts, tfexec.Replace(r))
	}
	if _, err := m.tf.Plan(ctx, append(opts, tfexec.Out(planFile))...); err != nil {
		return nil, fmt.Errorf("terraform plan %s: %w", m.Name, err)
	}
	p, err := m.tf.ShowPlanFile(ctx, planFile)
	if err != nil {
		return nil, fmt.Errorf("reading plan %s: %w", m.Name, err)
	}
	changes, err := intent.Changes(p)
	if err != nil {
		return nil, err
	}
	return &Plan{Module: m.Name, Env: m.env.Name, File: planFile, Changes: changes}, nil
}

// Apply applies the saved plan p. Terraform refuses it if the state changed
// since it was made.
func (m *Module) Apply(ctx context.Context, p *Plan) error {
	if p.Module != m.Name {
		return fmt.Errorf("plan for %s applied to %s", p.Module, m.Name)
	}
	if err := m.tf.Apply(ctx, tfexec.DirOrPlan(p.File)); err != nil {
		return fmt.Errorf("terraform apply %s: %w", m.Name, err)
	}
	return nil
}

// Output decodes the named output into v.
func (m *Module) Output(ctx context.Context, name string, v any) error {
	outs, err := m.tf.Output(ctx)
	if err != nil {
		return fmt.Errorf("terraform output %s: %w", m.Name, err)
	}
	raw, ok := outs[name]
	if !ok {
		return fmt.Errorf("%s has no output %q", m.Name, name)
	}
	return json.Unmarshal(raw.Value, v)
}

// Mode is how a command runs: Plan Mode by default, --apply to act.
type Mode struct {
	Apply           bool
	AllowUnexpected bool
}

// Applier applies a saved plan.
type Applier interface {
	Apply(ctx context.Context, p *Plan) error
}

// Converge plans the Module and hands the plan to Decide.
func Converge(ctx context.Context, m *Module, vars map[string]any, in intent.Intent, mode Mode, out io.Writer) (*Plan, bool, error) {
	p, err := m.Plan(ctx, vars)
	if err != nil {
		return nil, false, err
	}
	applied, err := Decide(ctx, m, p, in, mode, out)
	return p, applied, err
}

// Decide prints the plan and its check against in, and under --apply applies
// the saved plan. A mismatch warns in Plan Mode and aborts under --apply
// unless AllowUnexpected (ADR 0004). It reports whether it applied.
func Decide(ctx context.Context, a Applier, p *Plan, in intent.Intent, mode Mode, out io.Writer) (bool, error) {
	fmt.Fprintf(out, "plan %s (env %s): %s\n%s", p.Module, p.Env, in.Description, intent.Summary(p.Changes))

	if mm := in.Check(p.Changes); len(mm) > 0 {
		for _, x := range mm {
			fmt.Fprintf(out, "  ! %s\n", x)
		}
		switch {
		case !mode.Apply:
			fmt.Fprintf(out, "WARNING: the plan does not match the intent; --apply would abort.\n")
		case !mode.AllowUnexpected:
			return false, fmt.Errorf("%s: %w (%d mismatches); nothing applied", p.Module, ErrIntent, len(mm))
		default:
			fmt.Fprintf(out, "--allow-unexpected: applying despite %d mismatches above.\n", len(mm))
		}
	}
	if !mode.Apply || len(p.Changes) == 0 {
		return false, nil
	}
	if err := a.Apply(ctx, p); err != nil {
		return false, err
	}
	fmt.Fprintf(out, "applied %s: %d changes\n", p.Module, len(p.Changes))
	return true, nil
}
