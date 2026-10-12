package secrets

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/sops"
)

// TestRealSopsRoundTrip runs generate, rotate and read through the real
// sops binary with a throwaway age key: the files are fully encrypted, the
// key decrypts them, the copies follow, and nothing printed holds a value.
// Run it with an empty HOME under env -i to prove no key file helps.
func TestRealSopsRoundTrip(t *testing.T) {
	for _, bin := range []string{"sops", "age-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	root := t.TempDir()
	key, recipient := ageKey(t)
	config := filepath.Join(root, ".sops.yaml")
	rules := "creation_rules:\n  - path_regex: \\.enc\\.yaml$\n    encrypted_regex: ^(data|stringData)$\n    age: " + recipient + "\n"
	if err := os.WriteFile(config, []byte(rules), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sops", "--disable-version-check", "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	bin := sops.New(strings.Fields(string(out))[1], config)
	if err := bin.CheckVersion(ctx); err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Parse([]byte(testCatalog), root)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(cat, bin, func(context.Context) (string, error) { return key, nil })
	planner := NewPlanner(store, nil)

	var printed bytes.Buffer
	p, err := planner.Generate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.Print(&printed)
	if err := planner.Write(ctx, p); err != nil {
		t.Fatal(err)
	}
	db, _ := cat.Secret(catalog.Ref{Namespace: "db", Name: "creds"})
	app, _ := cat.Secret(catalog.Ref{Namespace: "app", Name: "credentials"})
	dbv, _, _, err := store.Read(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*catalog.Secret{db, app} {
		data, _ := os.ReadFile(cat.Path(s))
		if err := sops.CheckEncrypted(data); err != nil {
			t.Errorf("%s: %v", s.File, err)
		}
		if bytes.Contains(data, dbv["password"]) {
			t.Errorf("%s holds the password in the clear", s.File)
		}
	}

	p, err = planner.Rotate(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.Print(&printed)
	if err := planner.Write(ctx, p); err != nil {
		t.Fatal(err)
	}
	after, _, _, _ := store.Read(ctx, db)
	appv, _, _, err := store.Read(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(after["password"], dbv["password"]) || !bytes.Equal(appv["DB_PASS"], after["password"]) {
		t.Error("rotate did not change the password, or the copy did not follow")
	}
	rows, err := List(ctx, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	PrintList(&printed, rows)
	for _, v := range [][]byte{dbv["password"], after["password"]} {
		if bytes.Contains(printed.Bytes(), v) {
			t.Errorf("output holds a value:\n%s", printed.String())
		}
	}
}

func ageKey(t *testing.T) (key, recipient string) {
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
	return key, recipient
}
