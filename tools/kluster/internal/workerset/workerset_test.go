package workerset

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `# The Worker set. This comment must survive every edit.
workers = {
  # the first Worker
  k8swk1 = {
    private_ip  = "10.0.2.6"
    vpn_ip      = "10.100.0.2"
    server_type = "cx23"
    labels = {
      role = "worker"
    }
  }
  k8swk2 = {
    private_ip  = "10.0.2.7"
    vpn_ip      = "10.100.0.3"
    server_type = "cx23"
    labels = {
      role = "worker"
    }
  } # trailing note
}
`

func mustParse(t *testing.T, src string) *Set {
	t.Helper()
	s, err := Parse("terraform.tfvars", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The committed live set must load, or every live command fails.
func TestLoadCommittedSets(t *testing.T) {
	for _, p := range []string{"../../../../infra/workers/terraform.tfvars", "../../../../infra/workers/rehearsal.tfvars"} {
		if _, err := Load(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestWithAppendsAndKeepsComments(t *testing.T) {
	s := mustParse(t, sample)
	w := Worker{Name: "k8swk3", PrivateIP: "10.0.2.8", VpnIP: "10.100.0.4", ServerType: "cx23", Labels: map[string]string{"role": "worker"}}
	out, err := s.With(w)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), string(sample[:strings.Index(sample, "  } # trailing note")])) {
		t.Errorf("the edit changed text before the new entry:\n%s", out)
	}
	for _, c := range []string{"# The Worker set.", "# the first Worker", "# trailing note"} {
		if !strings.Contains(string(out), c) {
			t.Errorf("comment %q lost", c)
		}
	}
	got := mustParse(t, string(out))
	if g, _ := got.Get("k8swk3"); !equal(g, w) {
		t.Errorf("k8swk3 = %+v", g)
	}
	if _, err := s.With(w); err != nil {
		t.Fatal(err)
	}
	if _, err := got.With(w); err == nil {
		t.Error("a second entry for the same name was allowed")
	}
}

func TestWithIntoEmptySet(t *testing.T) {
	s := mustParse(t, "# empty on purpose\nworkers = {}\n")
	out, err := s.With(Worker{Name: "k8swk1", PrivateIP: "10.0.2.6", VpnIP: "10.100.0.2", ServerType: "cx23"})
	if err != nil {
		t.Fatal(err)
	}
	if got := mustParse(t, string(out)).Names(); len(got) != 1 || got[0] != "k8swk1" {
		t.Errorf("names = %v\n%s", got, out)
	}
}

func TestWithoutRemovesOnlyThatEntry(t *testing.T) {
	s := mustParse(t, sample)
	out, err := s.Without("k8swk1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "the first Worker") {
		t.Error("the removed entry's comment stayed")
	}
	for _, c := range []string{"# The Worker set.", "# trailing note"} {
		if !strings.Contains(string(out), c) {
			t.Errorf("comment %q lost", c)
		}
	}
	got := mustParse(t, string(out))
	if n := got.Names(); len(n) != 1 || n[0] != "k8swk2" {
		t.Errorf("names = %v", n)
	}
	// The last entry, whose line ends in a comment.
	out, err = got.Without("k8swk2")
	if err != nil {
		t.Fatal(err)
	}
	if n := mustParse(t, string(out)).Names(); len(n) != 0 {
		t.Errorf("names = %v\n%s", n, out)
	}
	if _, err := s.Without("nope"); err == nil {
		t.Error("removing an absent Worker was allowed")
	}
}

func TestNextTakesLowestFreeAndSkipsReserved(t *testing.T) {
	s := mustParse(t, strings.Replace(sample, `"10.0.2.6"`, `"10.0.2.9"`, 1))
	private, _ := ParsePool("10.0.2.6-10.0.2.254")
	vpn, _ := ParsePool("10.100.0.2-10.100.0.253")
	w, err := s.Next("k8swk3", "cx23", private, vpn, []netip.Addr{netip.MustParseAddr("10.100.0.4")})
	if err != nil {
		t.Fatal(err)
	}
	if w.PrivateIP != "10.0.2.6" || w.VpnIP != "10.100.0.5" {
		t.Errorf("next = %s, %s; want 10.0.2.6, 10.100.0.5", w.PrivateIP, w.VpnIP)
	}
	full, _ := ParsePool("10.0.2.7-10.0.2.7")
	if _, err := s.Next("x", "cx23", full, vpn, nil); err == nil {
		t.Error("a full pool gave an address")
	}
}

func TestCheckCommitted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	path := filepath.Join(dir, "rehearsal.tfvars")
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()

	write(sample)
	if err := CheckCommitted(ctx, path); !errors.Is(err, ErrUncommitted) {
		t.Fatalf("untracked: %v", err)
	}
	git("add", "rehearsal.tfvars")
	if err := CheckCommitted(ctx, path); !errors.Is(err, ErrUncommitted) {
		t.Fatalf("staged, not committed: %v", err)
	}
	git("commit", "-q", "-m", "set")
	if err := CheckCommitted(ctx, path); err != nil {
		t.Fatalf("committed: %v", err)
	}
	write(sample + "\n")
	if err := CheckCommitted(ctx, path); !errors.Is(err, ErrUncommitted) {
		t.Fatalf("edited: %v", err)
	}
}
