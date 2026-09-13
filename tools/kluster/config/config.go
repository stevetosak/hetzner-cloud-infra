// Package config loads and validates kluster.yaml.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the root of kluster.yaml.
type Config struct {
	ControlPlane ControlPlane `yaml:"controlPlane"`
	Node         Node         `yaml:"node"`
	WireGuard    WireGuard    `yaml:"wireguard"`
	Versions     Versions     `yaml:"versions"`
	Terraform    Terraform    `yaml:"terraform"`
}

type ControlPlane struct {
	PublicIP  string `yaml:"publicIP"`
	PrivateIP string `yaml:"privateIP"`
	VpnIP     string `yaml:"vpnIP"`
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
	CNIPlugins string `yaml:"cniPlugins"`
	Kubernetes string `yaml:"kubernetes"`
}

type Terraform struct {
	WorkDir string `yaml:"workDir"`
}

// Load reads and validates a kluster.yaml file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	cfg.applyDefaults()

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}

	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.ControlPlane.PublicIP == "" {
		c.ControlPlane.PublicIP = os.Getenv("CONTROL_PLANE_IP")
	}
	if c.ControlPlane.SSHUser == "" {
		c.ControlPlane.SSHUser = "root"
	}
	if c.Node.SSHUser == "" {
		c.Node.SSHUser = "root"
	}
	if c.WireGuard.Port == 0 {
		c.WireGuard.Port = 51820
	}
	if c.Terraform.WorkDir == "" {
		c.Terraform.WorkDir = "../../infra"
	}
}

func (c *Config) validate() error {
	var missing []string

	if c.ControlPlane.PublicIP == "" {
		missing = append(missing, "controlPlane.publicIP (or CONTROL_PLANE_IP env var)")
	}
	if c.ControlPlane.PrivateIP == "" {
		missing = append(missing, "controlPlane.privateIP")
	}
	if c.ControlPlane.VpnIP == "" {
		missing = append(missing, "controlPlane.vpnIP")
	}
	if c.Node.User == "" {
		missing = append(missing, "node.user")
	}
	if c.Node.NetworkInterface == "" {
		missing = append(missing, "node.networkInterface")
	}
	if c.WireGuard.Subnet == "" {
		missing = append(missing, "wireguard.subnet")
	}
	if c.Versions.Containerd == "" {
		missing = append(missing, "versions.containerd")
	}
	if c.Versions.Runc == "" {
		missing = append(missing, "versions.runc")
	}
	if c.Versions.CNIPlugins == "" {
		missing = append(missing, "versions.cniPlugins")
	}
	if c.Versions.Kubernetes == "" {
		missing = append(missing, "versions.kubernetes")
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing required fields: %v", missing)
	}

	return nil
}
