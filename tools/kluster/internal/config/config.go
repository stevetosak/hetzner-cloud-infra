// Package config loads and validates kluster.yaml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Module names. Each is one Terraform root Module with its own state
// (ADR 0002).
const (
	ModuleShared       = "shared"
	ModuleControlPlane = "control-plane"
	ModuleWorkers      = "workers"
)

// Environment names (ADR 0009).
const (
	EnvLive      = "live"
	EnvRehearsal = "rehearsal"
)

// Config is the root of kluster.yaml.
type Config struct {
	ControlPlane ControlPlane   `yaml:"controlPlane"`
	Node         Node           `yaml:"node"`
	WireGuard    WireGuard      `yaml:"wireguard"`
	Versions     Versions       `yaml:"versions"`
	Terraform    Terraform      `yaml:"terraform"`
	Envs         map[string]Env `yaml:"envs"`
	SSH          SSH            `yaml:"ssh"`

	// dir is the directory kluster.yaml was read from. Module paths are
	// relative to it.
	dir string
}

type ControlPlane struct {
	PrivateIP string `yaml:"privateIP"`
	VpnIP     string `yaml:"vpnIP"`
	Endpoint  string `yaml:"endpoint"`
	SSHUser   string `yaml:"sshUser"`
}

type Node struct {
	SSHUser          string `yaml:"sshUser"`
	User             string `yaml:"user"`
	NetworkInterface string `yaml:"networkInterface"`
}

type WireGuard struct {
	Subnet string `yaml:"subnet"`
	Port   int    `yaml:"port"`
	Peers  []Peer `yaml:"peers"`
}

type Peer struct {
	Name       string `yaml:"name"`
	PublicKey  string `yaml:"publicKey"`
	AllowedIPs string `yaml:"allowedIPs"`
}

type Versions struct {
	Containerd string `yaml:"containerd"`
	Runc       string `yaml:"runc"`
	Kubernetes string `yaml:"kubernetes"`
}

type Terraform struct {
	Modules map[string]string `yaml:"modules"`
}

// Env names the environment variables that hold one environment's
// credentials. It never holds a value.
type Env struct {
	HcloudTokenEnv       string `yaml:"hcloudTokenEnv"`
	R2AccessKeyIDEnv     string `yaml:"r2AccessKeyIDEnv"`
	R2SecretAccessKeyEnv string `yaml:"r2SecretAccessKeyEnv"`
	StateBucket          string `yaml:"stateBucket"`
	RefuseProjectWithIP  string `yaml:"refuseProjectWithIP"`
}

type SSH struct {
	IdentityFile string `yaml:"identityFile"`
}

// Load reads and validates the kluster.yaml file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	cfg.dir = filepath.Dir(abs)

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &cfg, nil
}

// ModuleDir returns the absolute directory of the named Terraform Module.
func (c *Config) ModuleDir(name string) (string, error) {
	rel, ok := c.Terraform.Modules[name]
	if !ok {
		return "", fmt.Errorf("unknown Terraform Module %q (known: %v)", name, c.moduleNames())
	}
	if filepath.IsAbs(rel) {
		return rel, nil
	}
	return filepath.Join(c.dir, rel), nil
}

// Env returns the named environment.
func (c *Config) Env(name string) (Env, error) {
	e, ok := c.Envs[name]
	if !ok {
		return Env{}, fmt.Errorf("unknown env %q", name)
	}
	return e, nil
}

func (c *Config) moduleNames() []string {
	names := make([]string, 0, len(c.Terraform.Modules))
	for n := range c.Terraform.Modules {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (c *Config) validate() error {
	var errs []error
	for _, m := range []string{ModuleShared, ModuleControlPlane, ModuleWorkers} {
		if c.Terraform.Modules[m] == "" {
			errs = append(errs, fmt.Errorf("terraform.modules.%s is required", m))
		}
	}
	for _, name := range []string{EnvLive, EnvRehearsal} {
		e, ok := c.Envs[name]
		if !ok {
			errs = append(errs, fmt.Errorf("envs.%s is required", name))
			continue
		}
		if e.HcloudTokenEnv == "" || e.R2AccessKeyIDEnv == "" || e.R2SecretAccessKeyEnv == "" {
			errs = append(errs, fmt.Errorf("envs.%s must name hcloudTokenEnv, r2AccessKeyIDEnv and r2SecretAccessKeyEnv", name))
		}
	}
	if r, ok := c.Envs[EnvRehearsal]; ok {
		// A rehearsal that shares the live bucket or credentials is not
		// isolated, whatever the code does with the keys (ADR 0009).
		if r.StateBucket == "" {
			errs = append(errs, errors.New("envs.rehearsal.stateBucket is required: rehearsal state never shares the live bucket"))
		}
		if r.RefuseProjectWithIP == "" {
			errs = append(errs, errors.New("envs.rehearsal.refuseProjectWithIP is required"))
		}
		if l, ok := c.Envs[EnvLive]; ok {
			if r.HcloudTokenEnv == l.HcloudTokenEnv || r.R2AccessKeyIDEnv == l.R2AccessKeyIDEnv ||
				r.R2SecretAccessKeyEnv == l.R2SecretAccessKeyEnv || r.StateBucket == l.StateBucket {
				errs = append(errs, errors.New("envs.rehearsal must name its own token, R2 keys and bucket, not the live ones"))
			}
		}
	}
	return errors.Join(errs...)
}
