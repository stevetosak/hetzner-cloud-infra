package sops

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// fake records each call and answers from its fields.
type fake struct {
	version string
	stdout  string
	stderr  string
	err     error
	env     []string
	stdin   string
	args    []string
}

func (f *fake) run(_ context.Context, env []string, stdin []byte, args ...string) ([]byte, []byte, error) {
	f.env, f.stdin, f.args = env, string(stdin), args
	if slices.Contains(args, "--version") {
		return []byte(f.version), nil, nil
	}
	return []byte(f.stdout), []byte(f.stderr), f.err
}

func TestCheckVersion(t *testing.T) {
	for _, tt := range []struct{ out, want string }{
		{"sops 3.13.3\n", ""},
		{"sops 3.13.3 (latest)\n", ""},
		{"sops 3.13.2\n", "sops 3.13.2 is installed; kluster.yaml pins 3.13.3"},
		{"garbage", "unreadable output"},
	} {
		f := &fake{version: tt.out}
		err := NewWithRunner(f.run, "3.13.3", "/repo/.sops.yaml").CheckVersion(context.Background())
		if (tt.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("%q: error %v, want %q", tt.out, err, tt.want)
		}
	}
}

func TestDecryptEnvironmentIsClean(t *testing.T) {
	t.Setenv("HOME", "/home/operator")
	t.Setenv("SOPS_AGE_KEY_FILE", "/home/operator/keys.txt")
	f := &fake{stdout: "plain"}
	b := NewWithRunner(f.run, "3.13.3", "/repo/.sops.yaml")
	out, err := b.Decrypt(context.Background(), []byte("cipher"), "core/x/x.enc.yaml", "AGE-SECRET-KEY-1TEST")
	if err != nil || string(out) != "plain" {
		t.Fatalf("Decrypt = %q, %v", out, err)
	}
	if len(f.env) != 2 || !strings.HasPrefix(f.env[0], "PATH=") || f.env[1] != "SOPS_AGE_KEY=AGE-SECRET-KEY-1TEST" {
		t.Errorf("env = %q, want PATH and SOPS_AGE_KEY only", f.env)
	}
	if f.stdin != "cipher" {
		t.Errorf("stdin = %q", f.stdin)
	}
	want := []string{"--disable-version-check", "--config", "/repo/.sops.yaml", "decrypt", "--filename-override",
		"core/x/x.enc.yaml", "--input-type", "yaml", "--output-type", "yaml", "/dev/stdin"}
	if !slices.Equal(f.args, want) {
		t.Errorf("args = %q", f.args)
	}
	if _, err := b.Decrypt(context.Background(), nil, "f", ""); err == nil {
		t.Error("Decrypt without a key passed")
	}
}

func TestEncryptWithholdsTheMessage(t *testing.T) {
	f := &fake{stderr: "error near: s3cr3t", err: errors.New("exit status 1")}
	b := NewWithRunner(f.run, "3.13.3", "/repo/.sops.yaml")
	_, err := b.Encrypt(context.Background(), []byte("password: s3cr3t"), "core/x/x.enc.yaml")
	if err == nil || strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("error %v: want a failure that does not quote the input", err)
	}
	if slices.ContainsFunc(f.env, func(e string) bool { return strings.HasPrefix(e, "SOPS_AGE_KEY") }) {
		t.Error("Encrypt passed an age key")
	}
}

// TestCommittedFilesAreEncrypted runs the check on every *.enc.yaml in the
// repository: the check must accept what sops wrote.
func TestCommittedFilesAreEncrypted(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(here), "..", "..", "..", "..")
	files, _ := filepath.Glob(filepath.Join(root, "*", "*", "*.enc.yaml"))
	more, _ := filepath.Glob(filepath.Join(root, "projects", "*", "*", "manifests", "*.enc.yaml"))
	files = append(files, more...)
	if len(files) == 0 {
		t.Fatal("no *.enc.yaml found")
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckEncrypted(data); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

const token = "ENC[AES256_GCM,data:aGk=,iv:aXY=,tag:dGFn,type:str]"

func TestCheckEncryptedRefuses(t *testing.T) {
	meta := "sops:\n  age:\n    - recipient: age1x\n  mac: " + token + "\n"
	for _, tt := range []struct{ name, doc, want string }{
		{"good", "data:\n  a: " + token + "\n" + meta, ""},
		{"good stringData", "stringData:\n  a: " + token + "\n" + meta, ""},
		{"plain value", "data:\n  a: " + token + "\n  b: hunter2\n" + meta, "data.b is not encrypted"},
		{"value not echoed", "stringData:\n  b: hunter2\n" + meta, "stringData.b"},
		{"no metadata", "data:\n  a: hunter2\n", "never encrypted"},
		{"no mac", "data:\n  a: " + token + "\nsops:\n  age:\n    - recipient: age1x\n", "no MAC"},
		{"no recipient", "data:\n  a: " + token + "\nsops:\n  mac: " + token + "\n", "no recipient"},
		{"no values", "metadata:\n  name: x\n" + meta, "no value"},
		{"nested", "data:\n  a:\n    b: " + token + "\n" + meta, "data.a is not encrypted"},
		{"not yaml", "data: [", "not YAML"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckEncrypted([]byte(tt.doc))
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v, want %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Errorf("error quotes a value: %v", err)
			}
		})
	}
}

// TestRoundTrip runs the real binary with a throwaway age key: encrypt needs
// no private key, the check accepts the result, decrypt returns the input
// with the right key and fails with another.
func TestRoundTrip(t *testing.T) {
	for _, bin := range []string{"sops", "age-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	ctx := context.Background()
	dir := t.TempDir()
	key, recipient := newAgeKey(t)
	other, _ := newAgeKey(t)
	config := filepath.Join(dir, ".sops.yaml")
	rules := "creation_rules:\n  - path_regex: \\.enc\\.yaml$\n    encrypted_regex: ^(data|stringData)$\n    age: " + recipient + "\n"
	if err := os.WriteFile(config, []byte(rules), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sops", "--disable-version-check", "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	b := New(strings.Fields(string(out))[1], config)
	if err := b.CheckVersion(ctx); err != nil {
		t.Fatal(err)
	}

	plain := "apiVersion: v1\nkind: Secret\nmetadata:\n    name: x\n    namespace: ns\ntype: Opaque\ndata:\n    password: aHVudGVyMg==\n"
	cipher, err := b.Encrypt(ctx, []byte(plain), "core/ns/x.enc.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cipher), "aHVudGVyMg==") {
		t.Fatal("the ciphertext holds the value")
	}
	if err := CheckEncrypted(cipher); err != nil {
		t.Fatalf("CheckEncrypted on sops output: %v", err)
	}
	got, err := b.Decrypt(ctx, cipher, "core/ns/x.enc.yaml", key)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != plain {
		t.Errorf("round trip:\n%s\nwant\n%s", got, plain)
	}
	if _, err := b.Decrypt(ctx, cipher, "core/ns/x.enc.yaml", other); err == nil {
		t.Error("decrypt with another key passed")
	}
}

func newAgeKey(t *testing.T) (key, recipient string) {
	t.Helper()
	out, err := exec.Command("age-keygen").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "AGE-SECRET-KEY-"):
			key = line
		case strings.HasPrefix(line, "# public key: "):
			recipient = strings.TrimPrefix(line, "# public key: ")
		}
	}
	if key == "" || recipient == "" {
		t.Fatal("age-keygen: unreadable output")
	}
	return key, recipient
}
