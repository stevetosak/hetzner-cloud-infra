package cmd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/env"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

func TestParseDBMode(t *testing.T) {
	for in, ok := range map[string]bool{"fresh": true, "recover": true, "": false, "none": false, "Fresh": false} {
		if _, err := parseDBMode(in); (err == nil) != ok {
			t.Errorf("--db %q: err = %v", in, err)
		}
	}
}

func TestSortServersRefusesWhatUpDoesNotBuildAround(t *testing.T) {
	set, err := workerset.Parse("t.tfvars", []byte(`workers = {
  k8swk1 = { private_ip = "10.0.2.6", vpn_ip = "10.100.0.2", server_type = "cx23", labels = {} }
  k8swk2 = { private_ip = "10.0.2.7", vpn_ip = "10.100.0.3", server_type = "cx23", labels = {} }
}`))
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	srv := func(name string, at time.Duration) *cloud.Server {
		return &cloud.Server{Name: name, Created: t0.Add(at)}
	}

	cp, workers, errs := sortServers([]*cloud.Server{srv("k8s-cp", 0), srv("k8swk1", time.Hour)}, "k8s-cp", set)
	if cp == nil || len(workers) != 1 || workers["k8swk1"] == nil || len(errs) != 0 {
		t.Fatalf("a Worker this build made: cp %v, workers %v, errs %v", cp, workers, errs)
	}
	for name, tc := range map[string]struct {
		servers []*cloud.Server
		want    string
	}{
		"a Worker without a Control Plane": {[]*cloud.Server{srv("k8swk1", 0)}, "a Worker of a lost cluster"},
		"a Worker older than the Control Plane": {[]*cloud.Server{srv("k8s-cp", time.Hour), srv("k8swk2", 0)},
			"a Worker of an earlier cluster"},
		"a server the set does not declare": {[]*cloud.Server{srv("k8s-cp", 0), srv("debug", time.Hour)}, "does not declare it"},
	} {
		_, workers, errs := sortServers(tc.servers, "k8s-cp", set)
		if len(errs) != 1 || !strings.Contains(errs[0].Error(), tc.want) || len(workers) != 0 {
			t.Errorf("%s: workers %v, errs %v; want %q", name, workers, errs, tc.want)
		}
	}
}

// One revision: HEAD must be the commit ArgoCD reads, and the paths up
// builds from hold no tracked change. An untracked file does not count.
func TestCheckRevision(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	ctx := context.Background()
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
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
	write("core/a.yaml", "a: 1\n")
	write("docs/x.md", "x\n")
	git("add", ".")
	git("commit", "-qm", "one")
	commit := git("rev-parse", "HEAD")

	a := &app{env: &env.Environment{Name: config.EnvRehearsal}, out: &bytes.Buffer{}}
	repo := gitRepo{root: root}
	tree := &syncedTree{rev: "rehearsal", commit: commit, head: commit}
	write("core/new.yaml", "untracked\n")
	write("docs/x.md", "changed outside the paths\n")
	if errs := a.checkRevision(ctx, repo, tree); len(errs) != 0 {
		t.Fatalf("clean paths refused: %v", errs)
	}

	write("core/a.yaml", "a: 2\n")
	other := &syncedTree{rev: "rehearsal", commit: strings.Repeat("0", 40), head: commit}
	errs := a.checkRevision(ctx, repo, other)
	if len(errs) != 2 ||
		!strings.Contains(errs[0].Error(), "git push -f origin HEAD:rehearsal") ||
		!strings.Contains(errs[1].Error(), "core/a.yaml") {
		t.Fatalf("errs = %v", errs)
	}
}
