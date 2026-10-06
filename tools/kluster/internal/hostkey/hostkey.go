// Package hostkey makes the SSH host key a new server boots with, so that the
// first login is verified instead of trusted (ADR 0009). The key travels in
// cloud-init user_data, which Hetzner keeps serving on the metadata service,
// so it is a bootstrap key only: the first login replaces it
// (docs/runbook/host-keys.md).
package hostkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

// KeyPair is an ed25519 SSH host key.
type KeyPair struct {
	PrivatePEM string
	Public     ssh.PublicKey
}

// Generate makes a new ed25519 host key pair.
func Generate() (*KeyPair, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating host key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, fmt.Errorf("encoding host key: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	return &KeyPair{PrivatePEM: string(pem.EncodeToMemory(block)), Public: signer.PublicKey()}, nil
}

// cloudConfig is the part of cloud-init kluster sets: the cc_ssh module, and
// cc_write_files for markers a Stage reads.
type cloudConfig struct {
	SSHDeleteKeys  bool              `yaml:"ssh_deletekeys"`
	SSHGenKeyTypes []string          `yaml:"ssh_genkeytypes"`
	SSHKeys        map[string]string `yaml:"ssh_keys"`
	WriteFiles     []File            `yaml:"write_files,omitempty"`
}

// File is one file cloud-init writes at first boot. It must hold nothing
// secret: Hetzner serves user_data to every process on the server.
type File struct {
	Path        string `yaml:"path"`
	Content     string `yaml:"content"`
	Permissions string `yaml:"permissions"`
}

// UserData renders the cloud-init user data that installs kp as the server's
// only host key: cloud-init deletes the image's keys, generates none, and
// writes this pair. It also writes files, if any.
func UserData(kp *KeyPair, files ...File) (string, error) {
	cc := cloudConfig{
		SSHDeleteKeys:  true,
		SSHGenKeyTypes: []string{},
		SSHKeys: map[string]string{
			"ed25519_private": kp.PrivatePEM,
			"ed25519_public":  AuthorizedKey(kp.Public),
		},
		WriteFiles: files,
	}
	body, err := yaml.Marshal(cc)
	if err != nil {
		return "", err
	}
	return "#cloud-config\n" + string(body), nil
}

// AuthorizedKey renders k as one `ssh-ed25519 AAAA…` line, without newline.
func AuthorizedKey(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}
