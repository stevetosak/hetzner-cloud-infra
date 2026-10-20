package env

import (
	"context"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load("../../kluster.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// shell is variables the operator's shell may hold. None of them may reach
// a child, and their values stand for live credentials.
var shell = map[string]string{
	"TF_VAR_HCLOUD_TOKEN":   "live-token",
	"AWS_ACCESS_KEY_ID":     "live-key",
	"AWS_SECRET_ACCESS_KEY": "live-secret",
}

// bothItems holds the live and the rehearsal item the committed config names.
func bothItems() Vault {
	return vaultOf(map[string][]string{"kluster/live": liveFields, "kluster/rehearsal": rehearsalFields})
}

// A rehearsal terraform must never see a live credential, whatever the shell
// holds.
func TestRehearsalTerraformEnvCarriesNoLiveCredential(t *testing.T) {
	e, err := Resolve(context.Background(), testConfig(t), config.EnvRehearsal, bothItems())
	if err != nil {
		t.Fatal(err)
	}
	base := []string{"PATH=/usr/bin", "HOME=/home/x", "TF_PLUGIN_CACHE_DIR=/c", "TF_LOG=TRACE",
		"HCLOUD_TOKEN=live-token", "TF_CLI_ARGS_plan=-target=x"}
	for k, v := range shell {
		base = append(base, k+"="+v)
	}
	got := e.TerraformEnv(base, "/repo/infra/shared")

	for k, v := range got {
		if strings.HasPrefix(v, "live-") {
			t.Errorf("%s carries a live credential", k)
		}
		if strings.HasPrefix(k, "TF_VAR_") || strings.HasPrefix(k, "TF_CLI_ARGS") || k == "TF_LOG" || k == "HCLOUD_TOKEN" {
			t.Errorf("%s reached the terraform environment", k)
		}
	}
	if got["AWS_ACCESS_KEY_ID"] != "reh-key" || got["AWS_SECRET_ACCESS_KEY"] != "reh-secret" {
		t.Error("rehearsal R2 keys are not set")
	}
	if got["TF_DATA_DIR"] != "/repo/infra/shared/.terraform-rehearsal" {
		t.Errorf("TF_DATA_DIR = %q", got["TF_DATA_DIR"])
	}
	if got["PATH"] != "/usr/bin" || got["TF_PLUGIN_CACHE_DIR"] != "/c" {
		t.Error("harmless variables were dropped")
	}
	if b := e.BackendConfig(); len(b) != 1 || b[0] != "bucket=hetzner-cloud-infra-staging" {
		t.Errorf("BackendConfig = %v", b)
	}
}

func TestLiveUsesBackendAsWritten(t *testing.T) {
	e, err := Resolve(context.Background(), testConfig(t), config.EnvLive, bothItems())
	if err != nil {
		t.Fatal(err)
	}
	if e.BackendConfig() != nil {
		t.Error("live must not override backend.tf")
	}
	if got := e.DataDir("/m"); got != "/m/.terraform" {
		t.Errorf("DataDir = %q", got)
	}
}

// The rehearsal item is checked against the live item's values.
func TestRehearsalRefusesTheLiveToken(t *testing.T) {
	v := vaultOf(map[string][]string{"kluster/live": liveFields, "kluster/rehearsal": {"hcloud_token=live-tf",
		"r2_access_key_id=reh-key", "r2_secret_access_key=reh-secret"}})
	if _, err := Resolve(context.Background(), testConfig(t), config.EnvRehearsal, v); err == nil {
		t.Fatal("a rehearsal item holding the live token was accepted")
	}
}

func TestResolveNamesMissingFields(t *testing.T) {
	v := vaultOf(map[string][]string{"kluster/live": {"hcloud_token=t", "r2_access_key_id=k"}})
	_, err := Resolve(context.Background(), testConfig(t), config.EnvLive, v)
	if err == nil || !strings.Contains(err.Error(), "r2_secret_access_key") {
		t.Fatalf("got %v", err)
	}
}

// `kluster exec`: the child gets this
// environment's token and keys, and nothing the shell held.
func TestExecEnv(t *testing.T) {
	base := []string{"PATH=/usr/bin", "TF_PLUGIN_CACHE_DIR=/c", "TF_DATA_DIR=/elsewhere",
		"HCLOUD_TOKEN=live-token", "BW_SESSION=open-vault", "TF_CLI_ARGS_init=-upgrade"}
	for k, v := range shell {
		base = append(base, k+"="+v)
	}

	t.Run("rehearsal", func(t *testing.T) {
		e, err := Resolve(context.Background(), testConfig(t), config.EnvRehearsal, bothItems())
		if err != nil {
			t.Fatal(err)
		}
		got := e.ExecEnv(base)
		for k, v := range got {
			if strings.HasPrefix(v, "live-") {
				t.Errorf("%s carries a live credential", k)
			}
		}
		want := map[string]string{
			"TF_VAR_HCLOUD_TOKEN":   "reh-tf",
			"AWS_ACCESS_KEY_ID":     "reh-key",
			"AWS_SECRET_ACCESS_KEY": "reh-secret",
			"TF_DATA_DIR":           ".terraform-rehearsal",
			"TF_CLI_ARGS_init":      "-backend-config=bucket=hetzner-cloud-infra-staging",
			"PATH":                  "/usr/bin",
			"TF_PLUGIN_CACHE_DIR":   "/c",
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s = %q, want %q", k, got[k], v)
			}
		}
		for _, k := range []string{"HCLOUD_TOKEN", "BW_SESSION"} {
			if _, ok := got[k]; ok {
				t.Errorf("%s reached the child", k)
			}
		}
	})

	t.Run("live", func(t *testing.T) {
		e, err := Resolve(context.Background(), testConfig(t), config.EnvLive, bothItems())
		if err != nil {
			t.Fatal(err)
		}
		got := e.ExecEnv(base)
		if got["TF_VAR_HCLOUD_TOKEN"] != "live-tf" || got["AWS_ACCESS_KEY_ID"] != "live-key" {
			t.Error("live credentials are not set")
		}
		for _, k := range []string{"TF_DATA_DIR", "TF_CLI_ARGS_init", "BW_SESSION"} {
			if _, ok := got[k]; ok {
				t.Errorf("%s is set for live; the Module's own .terraform and backend.tf apply", k)
			}
		}
	})
}
