package cmd

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/coreset"
)

// repoCopy copies the files services check reads into a temporary directory.
func repoCopy(t *testing.T) string {
	t.Helper()
	src := os.DirFS("../../..")
	dst := t.TempDir()
	copyFile := func(p string) error {
		data, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		return os.WriteFile(to, data, 0o644)
	}
	for _, top := range []string{"core", "projects"} {
		err := fs.WalkDir(src, top, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			return copyFile(p)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{coreset.AppsFile, catalog.FileName} {
		if err := copyFile(f); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func TestProjectAddPlanModeWritesNothing(t *testing.T) {
	root := repoCopy(t)
	var out bytes.Buffer
	if err := projectAdd(root, coreset.AddRequest{Project: "tosak-net"}, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "+          - path: projects/tosak-net/*/manifests/overlays/dev") {
		t.Errorf("Plan Mode must print the diff:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(root, "projects/tosak-net")); err == nil {
		t.Error("Plan Mode wrote projects/tosak-net")
	}
}

func TestProjectAddApplyWritesAndIsIdempotent(t *testing.T) {
	root := repoCopy(t)
	req := coreset.AddRequest{Project: "tosak-net", Component: "site", TagPrefix: "site-v"}
	var out bytes.Buffer
	if err := projectAdd(root, req, true, &out); err != nil {
		t.Fatal(err)
	}
	if errs := coreset.Check(os.DirFS(root)); len(errs) > 0 {
		t.Fatalf("the written tree fails the check: %v", errs)
	}
	out.Reset()
	if err := projectAdd(root, req, true, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "tosak-net is registered: nothing to change") {
		t.Errorf("a second run must change nothing:\n%s", out.String())
	}
}
