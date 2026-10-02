package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "kluster.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	return path
}

const validConfig = `
controlPlane:
  publicIP: "1.2.3.4"
  privateIP: "10.0.1.5"
  vpnIP: "10.100.0.1"
  sshUser: root

node:
  sshUser: root
  user: tosak
  networkInterface: enp7s0

wireguard:
  subnet: "10.100.0.0/24"
  port: 51820
  peers:
    - name: admin-laptop
      publicKey: "abc123"
      allowedIPs: "10.100.0.69/32"

versions:
  containerd: "2.2.0"
  runc: "1.4.0"
  cniPlugins: "1.9.0"
  kubernetes: "v1.34"

terraform:
  workDir: ../../infra
`

func TestLoad_Valid(t *testing.T) {
	path := writeTempConfig(t, validConfig)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.ControlPlane.PublicIP != "1.2.3.4" {
		t.Errorf("PublicIP = %q, want 1.2.3.4", cfg.ControlPlane.PublicIP)
	}
	if cfg.WireGuard.Port != 51820 {
		t.Errorf("WireGuard.Port = %d, want 51820", cfg.WireGuard.Port)
	}
	if len(cfg.WireGuard.Peers) != 1 || cfg.WireGuard.Peers[0].Name != "admin-laptop" {
		t.Errorf("WireGuard.Peers = %+v, want one peer named admin-laptop", cfg.WireGuard.Peers)
	}
}

func TestLoad_Defaults(t *testing.T) {
	const minimal = `
controlPlane:
  privateIP: "10.0.1.5"
  vpnIP: "10.100.0.1"
node:
  user: tosak
  networkInterface: enp7s0
wireguard:
  subnet: "10.100.0.0/24"
versions:
  containerd: "2.2.0"
  runc: "1.4.0"
  cniPlugins: "1.9.0"
  kubernetes: "v1.34"
`
	t.Setenv("CONTROL_PLANE_IP", "9.9.9.9")
	path := writeTempConfig(t, minimal)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.ControlPlane.PublicIP != "9.9.9.9" {
		t.Errorf("PublicIP fallback = %q, want 9.9.9.9 from env", cfg.ControlPlane.PublicIP)
	}
	if cfg.ControlPlane.SSHUser != "root" {
		t.Errorf("ControlPlane.SSHUser default = %q, want root", cfg.ControlPlane.SSHUser)
	}
	if cfg.Node.SSHUser != "root" {
		t.Errorf("Node.SSHUser default = %q, want root", cfg.Node.SSHUser)
	}
	if cfg.WireGuard.Port != 51820 {
		t.Errorf("WireGuard.Port default = %d, want 51820", cfg.WireGuard.Port)
	}
	if cfg.Terraform.WorkDir != "../../infra" {
		t.Errorf("Terraform.WorkDir default = %q, want ../../infra", cfg.Terraform.WorkDir)
	}
}

func TestLoad_MissingRequiredFields(t *testing.T) {
	const incomplete = `
node:
  networkInterface: enp7s0
`
	path := writeTempConfig(t, incomplete)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() expected error for missing required fields, got nil")
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("Load() expected error for missing file, got nil")
	}
}
