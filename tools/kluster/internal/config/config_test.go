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

// The rehearsal Worker set must be its own file, beside the live one in the
// workers Module, or a rehearsal `node add` writes the live set.
func TestWorkerSetsPerEnv(t *testing.T) {
	cfg, err := Load("../../kluster.yaml")
	if err != nil {
		t.Fatal(err)
	}
	mod, err := cfg.ModuleDir(ModuleWorkers)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{EnvLive: "terraform.tfvars", EnvRehearsal: "rehearsal.tfvars"}
	for env, file := range want {
		got, err := cfg.Path(cfg.Envs[env].WorkerSet)
		if err != nil {
			t.Fatal(err)
		}
		if got != filepath.Join(mod, file) {
			t.Errorf("%s worker set = %s, want %s", env, got, filepath.Join(mod, file))
		}
	}
}

// Each case changes one line of the committed file, so the refusal can only
// come from that line.
func TestValidateRefusesRehearsalSharingLive(t *testing.T) {
	committed, err := os.ReadFile("../../kluster.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]struct{ old, new, want string }{
		"shared token": {
			"hcloudTokenEnv: KLUSTER_REHEARSAL_HCLOUD_TOKEN", "hcloudTokenEnv: TF_VAR_HCLOUD_TOKEN",
			"its own token",
		},
		"no bucket": {
			"stateBucket: hetzner-cloud-infra-staging", `stateBucket: ""`, "stateBucket is required",
		},
		"no fingerprint": {
			`refuseProjectWithIP: "46.62.209.249"`, `refuseProjectWithIP: ""`, "refuseProjectWithIP is required",
		},
		"live wireguard config": {
			"wireguardConf: ~/.config/kluster/rehearsal/wg0.conf", "wireguardConf: /etc/wireguard/wg0.conf",
			"its own wireguardConf",
		},
		"live kubeconfig": {
			"kubeconfig: ~/.config/kluster/rehearsal/kubeconfig", "kubeconfig: ~/.kube/config",
			"its own wireguardConf, kubeconfig",
		},
		"live worker set": {
			"workerSet: ../../infra/workers/rehearsal.tfvars", "workerSet: ../../infra/workers/terraform.tfvars",
			"must name its own file",
		},
		"no worker set": {
			"workerSet: ../../infra/workers/rehearsal.tfvars", `workerSet: ""`,
			"envs.rehearsal.workerSet is required",
		},
		"sudo": {
			"wireguardConf: ~/.config/kluster/rehearsal/wg0.conf",
			"wireguardConf: ~/.config/kluster/rehearsal/wg0.conf\n      sudo: true",
			"must not use sudo",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(string(committed), tc.old) {
				t.Fatalf("the committed file no longer holds %q", tc.old)
			}
			body := strings.Replace(string(committed), tc.old, tc.new, 1)
			path := filepath.Join(t.TempDir(), "kluster.yaml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal naming %q, got %v", tc.want, err)
			}
		})
	}
}

func TestPathResolvesHomeAndRelative(t *testing.T) {
	cfg, err := Load("../../kluster.yaml")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	got, err := cfg.Path("~/.kube/config")
	if err != nil || got != filepath.Join(home, ".kube/config") {
		t.Errorf("~ path: got %q, %v", got, err)
	}
	flannel, err := cfg.Path(cfg.Manifests.Flannel)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(flannel); err != nil {
		t.Errorf("manifests.flannel does not resolve to a file: %v", err)
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
