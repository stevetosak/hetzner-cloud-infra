package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The committed kluster.yaml is the file every run reads, so it is the one
// the test loads.
func TestLoadCommittedConfig(t *testing.T) {
	cfg, err := Load("../../kluster.yaml")
	if err != nil {
		t.Fatalf("committed kluster.yaml does not load: %v", err)
	}
	dir, err := cfg.ModuleDir(ModuleWorkers)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "main.tf")); err != nil {
		t.Errorf("workers Module path does not resolve to a Module: %v", err)
	}
}

func TestValidateRefusesRehearsalSharingLive(t *testing.T) {
	tests := map[string]string{
		"shared token": `
envs:
  live: {hcloudTokenEnv: T, r2AccessKeyIDEnv: A, r2SecretAccessKeyEnv: S}
  rehearsal: {hcloudTokenEnv: T, r2AccessKeyIDEnv: RA, r2SecretAccessKeyEnv: RS, stateBucket: b, refuseProjectWithIP: 1.2.3.4}`,
		"no bucket": `
envs:
  live: {hcloudTokenEnv: T, r2AccessKeyIDEnv: A, r2SecretAccessKeyEnv: S}
  rehearsal: {hcloudTokenEnv: RT, r2AccessKeyIDEnv: RA, r2SecretAccessKeyEnv: RS, refuseProjectWithIP: 1.2.3.4}`,
		"no fingerprint": `
envs:
  live: {hcloudTokenEnv: T, r2AccessKeyIDEnv: A, r2SecretAccessKeyEnv: S}
  rehearsal: {hcloudTokenEnv: RT, r2AccessKeyIDEnv: RA, r2SecretAccessKeyEnv: RS, stateBucket: b}`,
	}
	modules := "terraform:\n  modules: {shared: s, control-plane: c, workers: w}\n"
	for name, envs := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "kluster.yaml")
			if err := os.WriteFile(path, []byte(modules+envs), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("Load accepted a rehearsal env that is not isolated")
			}
		})
	}
}

func TestLoadRefusesUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kluster.yaml")
	if err := os.WriteFile(path, []byte("terraform:\n  workDir: ../../infra\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "workDir") {
		t.Fatalf("a stale field must fail loudly, got %v", err)
	}
}
