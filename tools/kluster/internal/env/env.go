// Package env resolves where kluster acts — the live project or the rehearsal
// project — and builds the only environment a terraform child process sees
// (ADR 0009).
package env

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
)

// Environment holds one environment's credentials. The values are unexported
// and nothing here prints them.
type Environment struct {
	Name                string
	StateBucket         string
	RefuseProjectWithIP string

	hcloudToken string
	r2KeyID     string
	r2Secret    string
}

// Resolve reads the named environment's credentials through getenv.
func Resolve(cfg *config.Config, name string, getenv func(string) string) (*Environment, error) {
	e, err := cfg.Env(name)
	if err != nil {
		return nil, err
	}
	env := &Environment{
		Name:                name,
		StateBucket:         e.StateBucket,
		RefuseProjectWithIP: e.RefuseProjectWithIP,
		hcloudToken:         getenv(e.HcloudTokenEnv),
		r2KeyID:             getenv(e.R2AccessKeyIDEnv),
		r2Secret:            getenv(e.R2SecretAccessKeyEnv),
	}
	var errs []error
	for envName, v := range map[string]string{
		e.HcloudTokenEnv:       env.hcloudToken,
		e.R2AccessKeyIDEnv:     env.r2KeyID,
		e.R2SecretAccessKeyEnv: env.r2Secret,
	} {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is not set (env %s)", envName, name))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	if name != config.EnvLive {
		// The config names different variables; this checks they do not
		// hold the same values. A rehearsal token pasted from the live
		// project would otherwise pass every name check.
		live, _ := cfg.Env(config.EnvLive)
		if v := getenv(live.HcloudTokenEnv); v != "" && v == env.hcloudToken {
			return nil, fmt.Errorf("%s holds the live Hetzner token", e.HcloudTokenEnv)
		}
		if v := getenv(live.R2AccessKeyIDEnv); v != "" && v == env.r2KeyID {
			return nil, fmt.Errorf("%s holds the live R2 key", e.R2AccessKeyIDEnv)
		}
	}
	return env, nil
}

// IsLive reports whether this is the live environment.
func (e *Environment) IsLive() bool { return e.Name == config.EnvLive }

// HcloudToken returns the Hetzner API token for this environment.
func (e *Environment) HcloudToken() string { return e.hcloudToken }

// DataDir is the Terraform data directory for a Module. The live one is the
// default `.terraform`, so a hand-run terraform in the Module sees the same
// backend kluster uses. Every other environment has its own, so its init
// never repoints the live one.
func (e *Environment) DataDir(moduleDir string) string {
	if e.IsLive() {
		return filepath.Join(moduleDir, ".terraform")
	}
	return filepath.Join(moduleDir, ".terraform-"+e.Name)
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
// environment's credentials or change what terraform does.
var strippedPrefixes = []string{"TF_", "AWS_", "HCLOUD_"}

// keptTF are the TF_* variables that change only where providers are cached.
var keptTF = map[string]bool{"TF_PLUGIN_CACHE_DIR": true}

// TerraformEnv builds the environment of a terraform child process from base
// (normally os.Environ()). Every TF_*, AWS_* and HCLOUD_* variable is dropped,
// then this environment's R2 keys and data directory are set. The Hetzner
// token is not here: it goes in the run's var file.
func (e *Environment) TerraformEnv(base []string, moduleDir string) map[string]string {
	out := make(map[string]string, len(base))
	for _, kv := range base {
		k, v, _ := strings.Cut(kv, "=")
		if hasAnyPrefix(k, strippedPrefixes) && !keptTF[k] {
			continue
		}
		out[k] = v
	}
	out["AWS_ACCESS_KEY_ID"] = e.r2KeyID
	out["AWS_SECRET_ACCESS_KEY"] = e.r2Secret
	out["TF_DATA_DIR"] = e.DataDir(moduleDir)
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
