package cmd

import (
	"context"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/bitwarden/fakevault"
	"slices"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/env"
)

func TestCheckExecArgs(t *testing.T) {
	for _, c := range []struct {
		name    string
		dash    int
		acts    bool
		wantErr bool
	}{
		{"command after --", 0, false, false},
		{"no --", -1, false, true},
		{"words before --", 1, false, true},
		{"--apply", 0, true, true},
	} {
		if err := checkExecArgs(c.dash, c.acts); (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
}

func TestExecTarget(t *testing.T) {
	cfg, err := config.Load("../kluster.yaml")
	if err != nil {
		t.Fatal(err)
	}
	e, err := env.Resolve(context.Background(), cfg, config.EnvLive, fakevault.Of(map[string][]string{"kluster/live": {"hcloud_token=live-token", "r2_access_key_id=live-key", "r2_secret_access_key=live-secret"}}))
	if err != nil {
		t.Fatal(err)
	}
	path, envv, err := execTarget(e, "sh", []string{"PATH=/usr/bin:/bin", "BW_SESSION=open-vault"})
	if err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Error("sh not resolved to a path")
	}
	want := []string{"AWS_ACCESS_KEY_ID=live-key", "AWS_SECRET_ACCESS_KEY=live-secret",
		"PATH=/usr/bin:/bin", "TF_VAR_HCLOUD_TOKEN=live-token"}
	if !slices.Equal(envv, want) {
		t.Errorf("env = %v, want %v", envv, want)
	}
	if _, _, err := execTarget(e, "no-such-command-kluster", nil); err == nil {
		t.Error("a missing command must fail before kluster is replaced")
	}
}
