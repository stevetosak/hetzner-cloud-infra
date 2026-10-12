// Package sops encrypts and decrypts Secret manifests with the pinned `sops`
// binary, and checks in Go that a file is encrypted (ADR 0003, Amendment
// 2026-10-06).
//
// The binary is used for encryption and decryption because the SOPS Go module
// exposes decryption only, and pulls in every cloud KMS SDK. The check needs
// no binary, so CI needs only Go.
//
// Plaintext moves through pipes only, never a file. The age private key
// reaches the decrypting child as SOPS_AGE_KEY in its own environment, which
// holds nothing else from the operator's shell: no HOME, so sops cannot fall
// back to a key file on disk.
package sops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Runner runs sops with args, env as its whole environment and stdin as its
// input. Tests replace it.
type Runner func(ctx context.Context, env []string, stdin []byte, args ...string) (stdout, stderr []byte, err error)

// Binary is the pinned sops binary and the repository's .sops.yaml.
type Binary struct {
	run     Runner
	version string
	config  string
}

// New returns a Binary that runs `sops` from PATH. version is the exact
// release kluster.yaml pins (versions.sops); config is the path of
// .sops.yaml.
func New(version, config string) *Binary { return NewWithRunner(cli, version, config) }

// NewWithRunner returns a Binary over run, for tests.
func NewWithRunner(run Runner, version, config string) *Binary {
	return &Binary{run: run, version: version, config: config}
}

func cli(ctx context.Context, env []string, stdin []byte, args ...string) ([]byte, []byte, error) {
	bin, err := exec.LookPath("sops")
	if err != nil {
		return nil, nil, errors.New("`sops` is not installed (docs/runbook/secrets.md)")
	}
	c := exec.CommandContext(ctx, bin, args...)
	c.Env = env
	c.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	err = c.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// baseEnv is the whole environment of a sops child: PATH, so sops can run at
// all, and nothing else from the shell.
func baseEnv() []string { return []string{"PATH=" + os.Getenv("PATH")} }

// CheckVersion refuses any sops but the pinned release. 3.13.2 broke the MAC
// of files written by 3.13.1 (getsops/sops#2243), so a version change is made
// on purpose, in kluster.yaml.
func (b *Binary) CheckVersion(ctx context.Context) error {
	out, stderr, err := b.run(ctx, baseEnv(), nil, "--disable-version-check", "--version")
	if err != nil {
		return fmt.Errorf("sops --version: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[0] != "sops" {
		return fmt.Errorf("sops --version: unreadable output %q", firstLine(out))
	}
	if got := strings.TrimPrefix(fields[1], "v"); got != strings.TrimPrefix(b.version, "v") {
		return fmt.Errorf("sops %s is installed; kluster.yaml pins %s (versions.sops). Install that release, or change the pin on purpose", got, b.version)
	}
	return nil
}

// Encrypt encrypts a plaintext Secret manifest for file, its path in the
// repository: .sops.yaml picks the recipients and what to encrypt by that
// name. Encryption needs no private key.
func (b *Binary) Encrypt(ctx context.Context, plaintext []byte, file string) ([]byte, error) {
	out, _, err := b.run(ctx, baseEnv(), plaintext, b.args("encrypt", file)...)
	if err != nil {
		// sops's message can quote the input, which holds the values, so
		// it is withheld.
		return nil, fmt.Errorf("sops encrypt %s failed (%v); its message is withheld because the input holds values. Check .sops.yaml has a creation rule for the file", file, err)
	}
	return out, nil
}

// Decrypt decrypts the ciphertext of file with the age private key ageKey.
func (b *Binary) Decrypt(ctx context.Context, ciphertext []byte, file, ageKey string) ([]byte, error) {
	if ageKey == "" {
		return nil, errors.New("sops decrypt: no age key")
	}
	env := append(baseEnv(), "SOPS_AGE_KEY="+ageKey)
	out, stderr, err := b.run(ctx, env, ciphertext, b.args("decrypt", file)...)
	if err != nil {
		// The input is ciphertext, so sops's message holds no value.
		return nil, fmt.Errorf("sops decrypt %s: %w: %s", file, err, strings.TrimSpace(string(stderr)))
	}
	return out, nil
}

// args reads the input from stdin and names the file for the creation rules.
func (b *Binary) args(verb, file string) []string {
	return []string{"--disable-version-check", "--config", b.config, verb,
		"--filename-override", file, "--input-type", "yaml", "--output-type", "yaml", "/dev/stdin"}
}

func firstLine(b []byte) string {
	s, _, _ := strings.Cut(string(b), "\n")
	return s
}

// Edit opens file in sops edit on the operator's terminal. The editor gets
// TERM, HOME, EDITOR and VISUAL from the shell, and sops the age key; no
// other variable reaches the child. It reports whether the file changed.
func (b *Binary) Edit(ctx context.Context, file, ageKey string) (changed bool, err error) {
	if ageKey == "" {
		return false, errors.New("sops edit: no age key")
	}
	bin, err := exec.LookPath("sops")
	if err != nil {
		return false, errors.New("`sops` is not installed (docs/runbook/secrets.md)")
	}
	c := exec.CommandContext(ctx, bin, "--disable-version-check", "--config", b.config, "edit", file)
	c.Env = append(baseEnv(), "SOPS_AGE_KEY="+ageKey)
	for _, v := range []string{"TERM", "HOME", "EDITOR", "VISUAL"} {
		if s, ok := os.LookupEnv(v); ok {
			c.Env = append(c.Env, v+"="+s)
		}
	}
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = c.Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() == exitFileNotModified:
		return false, nil
	case err != nil:
		return false, fmt.Errorf("sops edit %s: %w", file, err)
	}
	return true, nil
}

// exitFileNotModified is the sops exit code of an edit that changed nothing.
const exitFileNotModified = 200
