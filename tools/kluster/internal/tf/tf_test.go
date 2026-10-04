package tf

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/env"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/intent"
)

// This test runs the real terraform binary against a throwaway Module with a
// local backend and the built-in terraform_data resource: no provider is
// downloaded and nothing outside t.TempDir() is touched. It still runs
// terraform, so it is opt-in: KLUSTER_TF_TEST=1.
const module = `
terraform {
  backend "local" {}
}
variable "HCLOUD_TOKEN" {
  type      = string
  sensitive = true
}
variable "n" {
  type    = number
  default = 1
}
resource "terraform_data" "a" {
  triggers_replace = var.n
}
`

const klusterYAML = `
terraform:
  modules: {shared: s, control-plane: c, workers: w}
envs:
  live: {hcloudTokenEnv: T, r2AccessKeyIDEnv: A, r2SecretAccessKeyEnv: S}
  rehearsal: {hcloudTokenEnv: RT, r2AccessKeyIDEnv: RA, r2SecretAccessKeyEnv: RS, stateBucket: b, refuseProjectWithIP: 1.2.3.4}
`

func openTestModule(t *testing.T) (*Module, *RunDir) {
	t.Helper()
	if os.Getenv("KLUSTER_TF_TEST") != "1" {
		t.Skip("runs terraform; set KLUSTER_TF_TEST=1")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "kluster.yaml")
	if err := os.WriteFile(cfgPath, []byte(klusterYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	e, err := env.Resolve(cfg, config.EnvLive, func(k string) string { return "x-" + k })
	if err != nil {
		t.Fatal(err)
	}
	run, err := NewRunDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	m, err := Open(context.Background(), "test", dir, e, run)
	if err != nil {
		t.Fatal(err)
	}
	return m, run
}

func TestConvergeAppliesOnlyAMatchingPlan(t *testing.T) {
	m, _ := openTestModule(t)
	ctx := context.Background()
	var out bytes.Buffer
	createA := intent.Intent{Description: "create a", Expectations: []intent.Expectation{
		{Address: "terraform_data.a", Actions: []intent.Action{intent.Create}, Required: true},
	}}

	// Plan Mode changes nothing, even when the plan matches.
	if _, applied, err := Converge(ctx, m, nil, createA, Mode{}, &out); err != nil || applied {
		t.Fatalf("plan mode: applied=%v err=%v", applied, err)
	}
	// --apply with a mismatching intent aborts and applies nothing.
	_, applied, err := Converge(ctx, m, nil, intent.Intent{Description: "nothing"}, Mode{Apply: true}, &out)
	if !errors.Is(err, ErrIntent) || applied {
		t.Fatalf("mismatch under --apply: applied=%v err=%v", applied, err)
	}
	if _, applied, err := Converge(ctx, m, nil, createA, Mode{Apply: true}, &out); err != nil || !applied {
		t.Fatalf("matching --apply: applied=%v err=%v", applied, err)
	}
	// Now the Module is converged: a clean intent holds.
	if _, _, err := Converge(ctx, m, nil, intent.Intent{}, Mode{Apply: true}, &out); err != nil {
		t.Fatalf("converged module is not clean: %v", err)
	}
	// A var change replaces, which a clean intent refuses.
	p, _, err := Converge(ctx, m, map[string]any{"n": 2}, intent.Intent{}, Mode{Apply: true}, &out)
	if !errors.Is(err, ErrIntent) {
		t.Fatalf("replace slipped through: %v", err)
	}
	if len(p.Changes) != 1 || p.Changes[0].Action != intent.Replace {
		t.Fatalf("changes = %v", p.Changes)
	}
	if bytes.Contains(out.Bytes(), []byte("x-T")) {
		t.Error("the token reached the output")
	}
}

func TestRunDirIsPrivateAndRemoved(t *testing.T) {
	run, err := NewRunDir()
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(run.path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("run dir mode %o", fi.Mode().Perm())
	}
	if err := run.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(run.path); !os.IsNotExist(err) {
		t.Error("run dir survives Close")
	}
}
