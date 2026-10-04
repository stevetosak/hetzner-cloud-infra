package env

import (
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

func lookup(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

var shell = map[string]string{
	"TF_VAR_HCLOUD_TOKEN":                    "live-token",
	"AWS_ACCESS_KEY_ID":                      "live-key",
	"AWS_SECRET_ACCESS_KEY":                  "live-secret",
	"KLUSTER_REHEARSAL_HCLOUD_TOKEN":         "rehearsal-token",
	"KLUSTER_REHEARSAL_R2_ACCESS_KEY_ID":     "rehearsal-key",
	"KLUSTER_REHEARSAL_R2_SECRET_ACCESS_KEY": "rehearsal-secret",
}

// A rehearsal terraform must never see a live credential, whatever the shell
// holds.
func TestRehearsalTerraformEnvCarriesNoLiveCredential(t *testing.T) {
	e, err := Resolve(testConfig(t), config.EnvRehearsal, lookup(shell))
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
	if got["AWS_ACCESS_KEY_ID"] != "rehearsal-key" || got["AWS_SECRET_ACCESS_KEY"] != "rehearsal-secret" {
		t.Error("rehearsal R2 keys are not set")
	}
	if got["TF_DATA_DIR"] != "/repo/infra/shared/.terraform-rehearsal" {
		t.Errorf("TF_DATA_DIR = %q", got["TF_DATA_DIR"])
	}
	if got["PATH"] != "/usr/bin" || got["TF_PLUGIN_CACHE_DIR"] != "/c" {
		t.Error("harmless variables were dropped")
	}
	if b := e.BackendConfig(); len(b) != 1 || b[0] != "bucket=hetzner-cloud-infra-rehearsal" {
		t.Errorf("BackendConfig = %v", b)
	}
}

func TestLiveUsesBackendAsWritten(t *testing.T) {
	e, err := Resolve(testConfig(t), config.EnvLive, lookup(shell))
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

func TestRehearsalRefusesTheLiveToken(t *testing.T) {
	s := map[string]string{}
	for k, v := range shell {
		s[k] = v
	}
	s["KLUSTER_REHEARSAL_HCLOUD_TOKEN"] = s["TF_VAR_HCLOUD_TOKEN"]
	if _, err := Resolve(testConfig(t), config.EnvRehearsal, lookup(s)); err == nil {
		t.Fatal("a rehearsal env holding the live token was accepted")
	}
}

func TestResolveNamesMissingVariables(t *testing.T) {
	_, err := Resolve(testConfig(t), config.EnvRehearsal, lookup(nil))
	if err == nil || !strings.Contains(err.Error(), "KLUSTER_REHEARSAL_HCLOUD_TOKEN") {
		t.Fatalf("got %v", err)
	}
}
