package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/secrets"
)

// TestGitFilesStagedAndWorkingTree proves check --staged reads the index,
// not the working tree, and that the working-tree check sees untracked
// files and skips deleted ones.
func TestGitFilesStagedAndWorkingTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	ctx := context.Background()
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(rel, data string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("a/x.enc.yaml", "committed")
	write("a/gone.enc.yaml", "committed")
	git("add", "a")
	git("commit", "-q", "-m", "init")

	write("a/x.enc.yaml", "staged")
	git("add", "a/x.enc.yaml")
	write("a/x.enc.yaml", "working tree")
	write("b/new.enc.yaml", "untracked")
	write("README.md", "not a secret")
	if err := os.Remove(filepath.Join(root, "a/gone.enc.yaml")); err != nil {
		t.Fatal(err)
	}

	g := gitRepo{root: root}
	if err := g.isTop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := (gitRepo{root: filepath.Join(root, "a")}).isTop(ctx); err == nil {
		t.Error("a subdirectory passed as the top")
	}

	show := func(fs []secrets.File) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.Path+"="+string(f.Data))
		}
		slices.Sort(out)
		return out
	}
	staged, err := g.files(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := show(staged), []string{"a/x.enc.yaml=staged"}; !slices.Equal(got, want) {
		t.Errorf("staged %q, want %q", got, want)
	}
	tree, err := g.files(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := show(tree), []string{"a/x.enc.yaml=working tree", "b/new.enc.yaml=untracked"}; !slices.Equal(got, want) {
		t.Errorf("working tree %q, want %q", got, want)
	}
}
