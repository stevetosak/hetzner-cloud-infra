package catalog

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const valid = `
secrets:
  redis/redis:
    file: core/redis/redis.enc.yaml
    keys:
      password: {kind: generated}
  pg-cluster/db-credentials:
    file: core/cnpg/db-credentials.enc.yaml
    type: kubernetes.io/basic-auth
    labels: {cnpg.io/reload: "true"}
    keys:
      username: {kind: literal, value: tosak}
      password: {kind: generated, length: 64}
  authos/credentials:
    file: projects/authos/api/manifests/credentials.enc.yaml
    keys:
      DB_PASS: {kind: copy, from: pg-cluster/db-credentials.password}
      REDIS_PASS: {kind: copy, from: redis/redis.password}
  doma/credentials:
    file: projects/doma/web/manifests/credentials.enc.yaml
    keys:
      DATABASE_URL: {kind: copy, from: pg-cluster/db-credentials.password, format: "postgresql://tosak:{value}@db/doma"}
      GOOGLE_CLIENT_SECRET: {kind: issued, item: kluster/doma, field: google_client_secret}
  authos/keystore:
    file: projects/authos/api/manifests/keystore.enc.yaml
    keys:
      keystore.p12: {kind: issued, item: kluster/authos-keystore, attachment: keystore.p12}
`

func TestParseValid(t *testing.T) {
	c, err := Parse([]byte(valid), "/repo")
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, s := range c.Secrets {
		refs = append(refs, s.Ref.String())
	}
	want := []string{"authos/credentials", "authos/keystore", "doma/credentials", "pg-cluster/db-credentials", "redis/redis"}
	if diff := cmp.Diff(want, refs); diff != "" {
		t.Errorf("order (-want +got):\n%s", diff)
	}

	redis, _ := c.Secret(Ref{"redis", "redis"})
	if redis.Type != DefaultType || redis.Keys["password"].Length != DefaultLength {
		t.Errorf("defaults: type %q, length %d", redis.Type, redis.Keys["password"].Length)
	}
	if got := c.Path(redis); got != filepath.Join("/repo", "core", "redis", "redis.enc.yaml") {
		t.Errorf("Path = %s", got)
	}
	db, _ := c.Secret(Ref{"pg-cluster", "db-credentials"})
	if db.Keys["password"].Length != 64 || db.Labels["cnpg.io/reload"] != "true" {
		t.Errorf("db-credentials not read as written: %+v", db)
	}

	copies := c.CopiesOf(KeyRef{Ref{"pg-cluster", "db-credentials"}, "password"})
	wantCopies := []KeyRef{{Ref{"authos", "credentials"}, "DB_PASS"}, {Ref{"doma", "credentials"}, "DATABASE_URL"}}
	if diff := cmp.Diff(wantCopies, copies); diff != "" {
		t.Errorf("CopiesOf (-want +got):\n%s", diff)
	}

	doma, _ := c.Secret(Ref{"doma", "credentials"})
	if got := string(doma.Keys["DATABASE_URL"].Render([]byte("pw"))); got != "postgresql://tosak:pw@db/doma" {
		t.Errorf("Render = %q", got)
	}
	authos, _ := c.Secret(Ref{"authos", "credentials"})
	if got := string(authos.Keys["DB_PASS"].Render([]byte("pw"))); got != "pw" {
		t.Errorf("Render without format = %q", got)
	}
	if s, ok := c.ByFile("projects/authos/api/manifests/keystore.enc.yaml"); !ok || s.Ref.Name != "keystore" {
		t.Errorf("ByFile = %v, %v", s, ok)
	}
}

func TestParseRefuses(t *testing.T) {
	entry := func(id, file, keys string) string {
		return "secrets:\n  " + id + ":\n    file: " + file + "\n    keys:\n" + keys
	}
	src := "  r/r:\n    file: core/r/r.enc.yaml\n    keys:\n      p: {kind: generated}\n      l: {kind: literal, value: x}\n  c/c:\n    file: core/c/c.enc.yaml\n    keys:\n      k: {kind: copy, from: r/r.p}\n"
	tests := []struct {
		name, yaml, want string
	}{
		{"bad ref", entry("noslash", "a/noslash.enc.yaml", "      k: {kind: generated}\n"), "<namespace>/<name>"},
		{"no file", "secrets:\n  a/b:\n    keys:\n      k: {kind: generated}\n", "file is required"},
		{"file name", entry("a/b", "core/a/other.enc.yaml", "      k: {kind: generated}\n"), "must be named b.enc.yaml"},
		{"file escapes", entry("a/b", "../b.enc.yaml", "      k: {kind: generated}\n"), "clean path"},
		{"no keys", "secrets:\n  a/b:\n    file: x/b.enc.yaml\n", "at least one key"},
		{"bad key name", entry("a/b", "x/b.enc.yaml", "      'k k': {kind: generated}\n"), "not a valid Secret key"},
		{"unknown kind", entry("a/b", "x/b.enc.yaml", "      k: {kind: plain}\n"), `kind "plain"`},
		{"unknown field", entry("a/b", "x/b.enc.yaml", "      k: {kind: generated, size: 3}\n"), "field size not found"},
		{"foreign field", entry("a/b", "x/b.enc.yaml", "      k: {kind: generated, item: x}\n"), "a generated key has no item"},
		{"short", entry("a/b", "x/b.enc.yaml", "      k: {kind: generated, length: 8}\n"), "outside 32 to 256"},
		{"issued no item", entry("a/b", "x/b.enc.yaml", "      k: {kind: issued, field: f}\n"), "names its Bitwarden item"},
		{"issued both", entry("a/b", "x/b.enc.yaml", "      k: {kind: issued, item: i, field: f, attachment: a}\n"), "not both"},
		{"issued neither", entry("a/b", "x/b.enc.yaml", "      k: {kind: issued, item: i}\n"), "not both"},
		{"literal empty", entry("a/b", "x/b.enc.yaml", "      k: {kind: literal}\n"), "has a value"},
		{"copy bad from", entry("a/b", "x/b.enc.yaml", "      k: {kind: copy, from: nope}\n"), "<namespace>/<name>.<key>"},
		{"copy missing", entry("a/b", "x/b.enc.yaml", "      k: {kind: copy, from: x/y.z}\n"), "does not hold"},
		{"copy format", "secrets:\n" + src + "  d/d:\n    file: x/d.enc.yaml\n    keys:\n      k: {kind: copy, from: r/r.p, format: nothing}\n", "exactly once"},
		{"copy of copy", "secrets:\n" + src + "  d/d:\n    file: x/d.enc.yaml\n    keys:\n      k: {kind: copy, from: c/c.k}\n", "a copy key"},
		{"copy of literal", "secrets:\n" + src + "  d/d:\n    file: x/d.enc.yaml\n    keys:\n      k: {kind: copy, from: r/r.l}\n", "a literal key"},
		{"rehearsal value", "secrets:\n  a/b:\n    file: x/b.enc.yaml\n    rehearsal: skip\n    keys:\n      k: {kind: generated}\n", `rehearsal: "skip"`},
		{"same file", "secrets:\n  a/b:\n    file: x/b.enc.yaml\n    keys:\n      k: {kind: generated}\n  c/b:\n    file: x/b.enc.yaml\n    keys:\n      k: {kind: generated}\n", "is also the file of"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml), "/repo")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestParseKeyRefDottedName(t *testing.T) {
	k, err := ParseKeyRef("authos/keystore.v2.keystore")
	if err != nil {
		t.Fatal(err)
	}
	if k.Secret.Name != "keystore.v2" || k.Key != "keystore" {
		t.Errorf("got %+v", k)
	}
}

// TestRepositoryCatalog loads the real secrets.yaml: every entry valid, and
// every committed *.enc.yaml listed, so the catalog cannot drift from the
// files it describes.
func TestRepositoryCatalog(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(here), "..", "..", "..", "..")
	c, err := Load(filepath.Join(root, FileName))
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, s := range c.Secrets {
		listed[s.File] = true
	}
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".terraform")) {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(p, EncSuffix) {
			rel, _ := filepath.Rel(root, p)
			if !listed[filepath.ToSlash(rel)] {
				t.Errorf("%s is not in %s", rel, FileName)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
