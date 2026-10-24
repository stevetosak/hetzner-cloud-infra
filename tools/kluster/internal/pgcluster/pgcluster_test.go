package pgcluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// repoTop is the repository top, four levels above this package.
var repoTop = filepath.Join("..", "..", "..", "..")

// copyCluster copies core/cnpg/cluster into a new repository top.
func copyCluster(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(repoTop, filepath.FromSlash(ClusterDir))
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		dst := filepath.Join(root, filepath.FromSlash(ClusterDir), rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestTheRepositoryOverlayPasses(t *testing.T) {
	if errs := CheckOverlay(os.DirFS(repoTop)); len(errs) > 0 {
		t.Fatal(errs)
	}
}

func spec(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestRender(t *testing.T) {
	root := copyCluster(t)
	fresh, err := Render(root, Fresh, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := spec(t, fresh)
	if _, ok := dig(f, "spec", "bootstrap", "initdb"); !ok {
		t.Error("fresh has no initdb")
	}
	if a := f["metadata"].(map[string]any)["annotations"].(map[string]any)[CreatedBy]; a != "kluster db create --fresh" {
		t.Errorf("annotation %v", a)
	}

	at := time.Date(2026, 10, 4, 10, 0, 0, 0, time.FixedZone("CEST", 2*3600))
	rec, err := Render(root, Recover, &at)
	if err != nil {
		t.Fatal(err)
	}
	r := spec(t, rec)
	if _, ok := dig(r, "spec", "bootstrap", "initdb"); ok {
		t.Error("recover keeps initdb")
	}
	tgt, _ := dig(r, "spec", "bootstrap", "recovery", "recoveryTarget")
	if tgt["targetTime"] != "2026-10-04T08:00:00Z" {
		t.Errorf("targetTime %v, want UTC", tgt["targetTime"])
	}
	s := string(rec)
	if !strings.Contains(s, "serverName: tosak-pg-cluster-g2") || !strings.Contains(s, "barmanObjectName: "+SourceStore) {
		t.Errorf("recover render:\n%s", s)
	}
	if _, err := Render(root, Fresh, &at); err == nil {
		t.Error("a fresh Cluster took a target time")
	}
}

// TestKrustyRunsTestOps proves the overlay's test ops guard the build: a
// base on another generation fails instead of reading the wrong one.
func TestKrustyRunsTestOps(t *testing.T) {
	root := copyCluster(t)
	cf := filepath.Join(root, filepath.FromSlash(ClusterFile))
	b, _ := os.ReadFile(cf)
	os.WriteFile(cf, []byte(strings.Replace(string(b), "serverName: tosak-pg-cluster-g1", "serverName: tosak-pg-cluster-g2", 1)), 0o644)
	if _, err := Render(root, Recover, nil); err == nil || !strings.Contains(err.Error(), "test") {
		t.Fatalf("a base on g2 built: %v", err)
	}
	if errs := CheckOverlay(os.DirFS(root)); len(errs) == 0 {
		t.Error("the check passed an overlay one generation behind")
	}
}

func TestBump(t *testing.T) {
	root := copyCluster(t)
	n, err := Bump(root)
	if err != nil || n != 2 {
		t.Fatalf("Bump = %v, %v", n, err)
	}
	if g, _ := Current(os.DirFS(root)); g != 2 {
		t.Errorf("Current = %v after the bump", g)
	}
	rec, err := Render(root, Recover, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(rec); !strings.Contains(s, "serverName: tosak-pg-cluster-g3") || !strings.Contains(s, "source: tosak-pg-cluster-g2") {
		t.Errorf("after the bump:\n%s", s)
	}
	b, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(ClusterFile)))
	if !strings.Contains(string(b), "# The archive path's generation.") {
		t.Error("the bump lost a comment")
	}
}

func TestCheckOverlayRefuses(t *testing.T) {
	for _, c := range []struct{ name, old, new, want string }{
		{"the archive store as source", `"barmanObjectName": "tosak-pg-backups-source"`, `"barmanObjectName": "tosak-pg-backups"`, "source store"},
		{"a skipped generation", `"value": "tosak-pg-cluster-g2"`, `"value": "tosak-pg-cluster-g3"`, "next generation"},
		{"another source", `"source": "tosak-pg-cluster-g1"`, `"source": "tosak-pg-cluster-g0"`, "bootstrap.recovery.source"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := copyCluster(t)
			rf := filepath.Join(root, filepath.FromSlash(RecoverPatch))
			b, _ := os.ReadFile(rf)
			if !strings.Contains(string(b), c.old) {
				t.Fatalf("%q not in recover.json", c.old)
			}
			os.WriteFile(rf, []byte(strings.Replace(string(b), c.old, c.new, 1)), 0o644)
			errs := CheckOverlay(os.DirFS(root))
			if len(errs) == 0 || !strings.Contains(errs[0].Error(), c.want) {
				t.Errorf("errs %v, want %q", errs, c.want)
			}
		})
	}
}

func TestStoresFromTheRepository(t *testing.T) {
	for _, tc := range []struct {
		live    bool
		archive string
	}{{true, "tosak-pg-backups"}, {false, "tosak-pg-backups-rehearsal"}} {
		a, s, err := Stores(repoTop, tc.live)
		if err != nil {
			t.Fatal(err)
		}
		if a.Bucket != tc.archive || a.Secret != "r2-backup-credentials" {
			t.Errorf("live=%v archive = %+v", tc.live, a)
		}
		if s.Bucket != "tosak-pg-backups" || s.Secret != "r2-source-credentials" {
			t.Errorf("live=%v source = %+v", tc.live, s)
		}
		if a.AccessKey != "ACCESS_KEY_ID" || a.SecretKey != "ACCESS_SECRET_KEY" || len(a.Account()) != 32 {
			t.Errorf("live=%v keys or account: %+v %q", tc.live, a, a.Account())
		}
	}
}
