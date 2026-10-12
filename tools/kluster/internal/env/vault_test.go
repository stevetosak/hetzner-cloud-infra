package env

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/bitwarden"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
)

// vaultConfig is the committed config with both environments on Bitwarden.
func vaultConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := testConfig(t)
	for name, item := range map[string]string{config.EnvLive: "kluster/live", config.EnvRehearsal: "kluster/rehearsal"} {
		e := cfg.Envs[name]
		e.BitwardenItem = item
		e.HcloudTokenEnv, e.R2AccessKeyIDEnv, e.R2SecretAccessKeyEnv = "", "", ""
		cfg.Envs[name] = e
	}
	return cfg
}

// vaultOf answers rbw with one item per name; fields are name=value pairs.
func vaultOf(items map[string][]string) Vault {
	var names []string
	for name := range items {
		names = append(names, name)
	}
	run := func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "unlocked", "sync":
			return nil, nil
		case "list":
			var rows []string
			for i, name := range names {
				rows = append(rows, `{"id":"id`+strconv.Itoa(i)+`","name":"`+name+`"}`)
			}
			return []byte("[" + strings.Join(rows, ",") + "]"), nil
		case "get":
			i, _ := strconv.Atoi(strings.TrimPrefix(args[2], "id"))
			var fs []string
			for _, f := range items[names[i]] {
				k, v, _ := strings.Cut(f, "=")
				fs = append(fs, `{"name":"`+k+`","value":"`+v+`"}`)
			}
			return []byte(`{"id":"` + args[2] + `","name":"` + names[i] + `","fields":[` + strings.Join(fs, ",") + `]}`), nil
		}
		return nil, errors.New("unexpected call")
	}
	noBw := func(context.Context, ...string) ([]byte, error) { return nil, errors.New("bw is not used here") }
	return bitwarden.NewWithRunners(run, noBw, &bytes.Buffer{})
}

var (
	liveFields      = []string{"hcloud_token=live-tf", "hcloud_cluster_token=live-cl", "r2_access_key_id=live-key", "r2_secret_access_key=live-secret"}
	rehearsalFields = []string{"hcloud_token=reh-tf", "hcloud_cluster_token=reh-cl", "r2_access_key_id=reh-key", "r2_secret_access_key=reh-secret"}
)

func TestResolveFromVault(t *testing.T) {
	v := vaultOf(map[string][]string{"kluster/live": liveFields, "kluster/rehearsal": rehearsalFields})
	e, err := Resolve(context.Background(), vaultConfig(t), config.EnvRehearsal, lookup(nil), v)
	if err != nil {
		t.Fatal(err)
	}
	if tok, err := e.HcloudClusterToken(); e.HcloudToken() != "reh-tf" || tok != "reh-cl" || err != nil {
		t.Errorf("tokens = %q, %q, %v", e.HcloudToken(), tok, err)
	}
	if got := e.TerraformEnv(nil, "/m"); got["AWS_ACCESS_KEY_ID"] != "reh-key" || got["AWS_SECRET_ACCESS_KEY"] != "reh-secret" {
		t.Error("R2 keys are not the rehearsal item's")
	}
}

// A rehearsal item holding any live token or key is refused, whichever
// field it was pasted into.
func TestRehearsalItemWithALiveValueIsRefused(t *testing.T) {
	for _, bad := range []string{"hcloud_token=live-tf", "hcloud_token=live-cl", "hcloud_cluster_token=live-cl",
		"hcloud_cluster_token=live-tf", "r2_access_key_id=live-key"} {
		k, _, _ := strings.Cut(bad, "=")
		fields := []string{bad}
		for _, f := range rehearsalFields {
			if !strings.HasPrefix(f, k+"=") {
				fields = append(fields, f)
			}
		}
		v := vaultOf(map[string][]string{"kluster/live": liveFields, "kluster/rehearsal": fields})
		if _, err := Resolve(context.Background(), vaultConfig(t), config.EnvRehearsal, lookup(nil), v); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}

func TestClusterTokenMustDifferFromTheTerraformToken(t *testing.T) {
	v := vaultOf(map[string][]string{"kluster/live": {"hcloud_token=t", "hcloud_cluster_token=t", "r2_access_key_id=k", "r2_secret_access_key=s"}})
	_, err := Resolve(context.Background(), vaultConfig(t), config.EnvLive, lookup(nil), v)
	if err == nil || !strings.Contains(err.Error(), "token of its own") {
		t.Fatalf("err = %v", err)
	}
}

// Before the live item exists there is nothing to collide with; a missing
// cluster token fails only where it is needed.
func TestRehearsalWithoutALiveItem(t *testing.T) {
	v := vaultOf(map[string][]string{"kluster/rehearsal": rehearsalFields[:1:1]})
	if _, err := Resolve(context.Background(), vaultConfig(t), config.EnvRehearsal, lookup(nil), v); err == nil {
		t.Fatal("an item without the R2 keys was accepted")
	}
	v = vaultOf(map[string][]string{"kluster/rehearsal": {"hcloud_token=a", "r2_access_key_id=b", "r2_secret_access_key=c"}})
	e, err := Resolve(context.Background(), vaultConfig(t), config.EnvRehearsal, lookup(nil), v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.HcloudClusterToken(); err == nil {
		t.Error("a missing cluster token was not reported")
	}
}
