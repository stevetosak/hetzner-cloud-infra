// Package env resolves where kluster acts — the live project or the rehearsal
// project — and builds the only environment a terraform child process sees
// (ADR 0009).
package env

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/bitwarden"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
)

// Environment holds one environment's credentials. The values are unexported
// and nothing here prints them.
type Environment struct {
	Name                string
	StateBucket         string
	RefuseProjectWithIP string

	creds credentials
}

// credentials are the values one environment needs. clusterToken is the
// Hetzner token the cluster itself holds (kube-system/hcloud), apart from the
// one Terraform and kluster use, so either can be revoked alone.
type credentials struct {
	hcloudToken  string
	clusterToken string
	r2KeyID      string
	r2Secret     string
}

// The field names in an environment's Bitwarden item. Both environments use
// the same names, so a value in the wrong item is visible side by side.
const (
	fieldHcloudToken  = "hcloud_token"
	fieldClusterToken = "hcloud_cluster_token"
	fieldR2KeyID      = "r2_access_key_id"
	fieldR2Secret     = "r2_secret_access_key"
)

// Vault reads one item of the password manager.
type Vault interface {
	Item(ctx context.Context, name string) (*bitwarden.Item, error)
}

// Resolve reads the named environment's credentials, from its Bitwarden item
// when it names one, else through getenv.
func Resolve(ctx context.Context, cfg *config.Config, name string, getenv func(string) string, vault Vault) (*Environment, error) {
	e, err := cfg.Env(name)
	if err != nil {
		return nil, err
	}
	creds, err := read(ctx, e, name, getenv, vault)
	if err != nil {
		return nil, err
	}
	if creds.clusterToken != "" && creds.clusterToken == creds.hcloudToken {
		return nil, fmt.Errorf("env %s: %s is the Terraform token; the cluster needs a token of its own", name, fieldClusterToken)
	}
	if name != config.EnvLive {
		if err := notLive(ctx, cfg, creds, getenv, vault); err != nil {
			return nil, err
		}
	}
	return &Environment{
		Name:                name,
		StateBucket:         e.StateBucket,
		RefuseProjectWithIP: e.RefuseProjectWithIP,
		creds:               creds,
	}, nil
}

func read(ctx context.Context, e config.Env, name string, getenv func(string) string, vault Vault) (credentials, error) {
	if e.BitwardenItem != "" {
		return fromVault(ctx, vault, e.BitwardenItem)
	}
	c := credentials{
		hcloudToken: getenv(e.HcloudTokenEnv),
		r2KeyID:     getenv(e.R2AccessKeyIDEnv),
		r2Secret:    getenv(e.R2SecretAccessKeyEnv),
	}
	var errs []error
	for envName, v := range map[string]string{
		e.HcloudTokenEnv:       c.hcloudToken,
		e.R2AccessKeyIDEnv:     c.r2KeyID,
		e.R2SecretAccessKeyEnv: c.r2Secret,
	} {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is not set (env %s)", envName, name))
		}
	}
	return c, errors.Join(errs...)
}

// fromVault reads an item. The cluster token is optional here: only a
// Control Plane build needs it, and it says so when it is missing.
func fromVault(ctx context.Context, vault Vault, item string) (credentials, error) {
	it, err := vault.Item(ctx, item)
	if err != nil {
		return credentials{}, err
	}
	var c credentials
	var errs []error
	for _, f := range []struct {
		name string
		dst  *string
	}{{fieldHcloudToken, &c.hcloudToken}, {fieldR2KeyID, &c.r2KeyID}, {fieldR2Secret, &c.r2Secret}} {
		v, err := it.Field(f.name)
		errs = append(errs, err)
		*f.dst = v
	}
	c.clusterToken, _ = it.Field(fieldClusterToken)
	return c, errors.Join(errs...)
}

// notLive checks a rehearsal does not hold a live value. The config already
// names different items or variables; this checks the values, because a
// token pasted from the live project would pass every name check. A live
// item that does not exist yet has nothing to collide with.
func notLive(ctx context.Context, cfg *config.Config, c credentials, getenv func(string) string, vault Vault) error {
	le, _ := cfg.Env(config.EnvLive)
	live, err := read(ctx, le, config.EnvLive, getenv, vault)
	if errors.Is(err, bitwarden.ErrNotFound) {
		return nil
	}
	// Any other failure leaves only the values it did read to compare; an
	// empty one matches nothing.
	same := func(a, b string) bool { return a != "" && a == b }
	switch {
	case same(live.hcloudToken, c.hcloudToken), same(live.clusterToken, c.hcloudToken):
		return errors.New("the rehearsal Hetzner token is a live token")
	case same(live.clusterToken, c.clusterToken), same(live.hcloudToken, c.clusterToken):
		return errors.New("the rehearsal cluster token is a live token")
	case same(live.r2KeyID, c.r2KeyID):
		return errors.New("the rehearsal R2 key is the live key")
	}
	return nil
}

// IsLive reports whether this is the live environment.
func (e *Environment) IsLive() bool { return e.Name == config.EnvLive }

// HcloudToken returns the Hetzner API token for this environment.
func (e *Environment) HcloudToken() string { return e.creds.hcloudToken }

// HcloudClusterToken returns the Hetzner token the cluster holds, for the
// cloud controller and the CSI driver.
func (e *Environment) HcloudClusterToken() (string, error) {
	if e.creds.clusterToken == "" {
		return "", fmt.Errorf("env %s has no %s: add one to its Bitwarden item (a token of its own, not the Terraform one)", e.Name, fieldClusterToken)
	}
	return e.creds.clusterToken, nil
}

// DataDir is the Terraform data directory for a Module. The live one is the
// default `.terraform`, so a hand-run terraform in the Module sees the same
// backend kluster uses. Every other environment has its own, so its init
// never repoints the live one.
func (e *Environment) DataDir(moduleDir string) string {
	return filepath.Join(moduleDir, e.dataDirName())
}

func (e *Environment) dataDirName() string {
	if e.IsLive() {
		return ".terraform"
	}
	return ".terraform-" + e.Name
}

// BackendConfig returns the -backend-config overrides for this environment.
// Live uses each Module's backend.tf as written.
func (e *Environment) BackendConfig() []string {
	if e.IsLive() || e.StateBucket == "" {
		return nil
	}
	return []string{"bucket=" + e.StateBucket}
}

// strippedPrefixes are the shell variables that could carry another
// environment's credentials, change what terraform does, or open the vault
// (BW_SESSION): no child process reads the password manager.
var strippedPrefixes = []string{"TF_", "AWS_", "HCLOUD_", "BW_"}

// keptTF are the TF_* variables that change only where providers are cached.
var keptTF = map[string]bool{"TF_PLUGIN_CACHE_DIR": true}

// TerraformEnv builds the environment of a terraform child process from base
// (normally os.Environ()): the clean base, this environment's R2 keys and
// data directory. The Hetzner token is not here: it goes in the run's var
// file.
func (e *Environment) TerraformEnv(base []string, moduleDir string) map[string]string {
	out := clean(base)
	e.setR2(out)
	out["TF_DATA_DIR"] = e.DataDir(moduleDir)
	return out
}

// ExecEnv builds the environment of a command the operator runs with
// `kluster exec`, in place of sourcing infra/.envrc: the clean base, the R2
// keys and the Hetzner token as TF_VAR_HCLOUD_TOKEN. Outside live it also
// points terraform at this environment's data directory and state bucket;
// both are relative to the Module, so `terraform -chdir=<module>` works.
// The cluster token is not here: only a Control Plane build hands it on.
func (e *Environment) ExecEnv(base []string) map[string]string {
	out := clean(base)
	e.setR2(out)
	out["TF_VAR_HCLOUD_TOKEN"] = e.creds.hcloudToken
	if e.IsLive() {
		return out
	}
	out["TF_DATA_DIR"] = e.dataDirName()
	var args []string
	for _, b := range e.BackendConfig() {
		args = append(args, "-backend-config="+b)
	}
	if len(args) > 0 {
		out["TF_CLI_ARGS_init"] = strings.Join(args, " ")
	}
	return out
}

// clean copies base without the stripped variables.
func clean(base []string) map[string]string {
	out := make(map[string]string, len(base))
	for _, kv := range base {
		k, v, _ := strings.Cut(kv, "=")
		if hasAnyPrefix(k, strippedPrefixes) && !keptTF[k] {
			continue
		}
		out[k] = v
	}
	return out
}

func (e *Environment) setR2(env map[string]string) {
	env["AWS_ACCESS_KEY_ID"] = e.creds.r2KeyID
	env["AWS_SECRET_ACCESS_KEY"] = e.creds.r2Secret
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
