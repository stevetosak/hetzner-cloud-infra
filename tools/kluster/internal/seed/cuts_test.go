package seed

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/coreset"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// Every rehearsal cut has its entry in RehearsalCuts, and every entry is a
// cut: a new cut cannot land without saying which live links it removes.
func TestEveryRehearsalCutHasAnEntry(t *testing.T) {
	dirs, err := os.ReadDir(filepath.Join(repoRoot, filepath.FromSlash(coreset.RehearsalDir)))
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]Cut{}
	for _, c := range RehearsalCuts {
		entries[c.Dir] = c
		if len(c.Links) == 0 && c.Why == "" {
			t.Errorf("core/rehearsal/%s: no live link and no reason", c.Dir)
		}
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		if _, ok := entries[d.Name()]; !ok {
			t.Errorf("core/rehearsal/%s has no entry in RehearsalCuts", d.Name())
		}
		delete(entries, d.Name())
	}
	for dir := range entries {
		t.Errorf("RehearsalCuts names core/rehearsal/%s, which does not exist", dir)
	}
}

// cluster answers the reads of the rehearsal-cuts Stage from objects keyed
// by kind/name.
type cluster map[string]string

func (c cluster) Run(_ context.Context, cmd string) (string, error) {
	for key, obj := range c {
		kind, name, _ := strings.Cut(key, "/")
		if strings.Contains(cmd, "'"+kind+"' '"+name+"'") {
			return obj, nil
		}
	}
	return "", nil
}
func (cluster) WriteFile(context.Context, string, string, os.FileMode) error { return nil }
func (cluster) ReadFile(context.Context, string) (string, error)             { return "", nil }

func TestCutsHoldInARehearsal(t *testing.T) {
	c := &Cuts{Bucket: "tosak-pg-backups-rehearsal"}
	h := &stage.Host{Name: "cp", Exec: cluster{
		"deployments.apps/argocd-notifications-controller":  `{"spec":{"replicas":0}}`,
		"clusterissuers.cert-manager.io/letsencrypt-prod":   `{"spec":{"selfSigned":{}}}`,
		"objectstores.barmancloud.cnpg.io/tosak-pg-backups": `{"spec":{"configuration":{"destinationPath":"s3://tosak-pg-backups-rehearsal/"}}}`,
	}}
	st, err := c.Probe(context.Background(), h)
	if err != nil || !st.Done {
		t.Fatalf("%+v %v", st, err)
	}
}

// The negative test: a cluster as live is, every link in place. The Stage
// names each one and stops the run.
func TestCutsNameEveryLiveLink(t *testing.T) {
	c := &Cuts{Bucket: "tosak-pg-backups-rehearsal"}
	h := &stage.Host{Name: "cp", Exec: cluster{
		"deployments.apps/argocd-notifications-controller":  `{"spec":{"replicas":1}}`,
		"secrets/argocd-notifications-secret":               "secret/argocd-notifications-secret",
		"applicationsets.argoproj.io/projects":              `{}`,
		"clusterissuers.cert-manager.io/letsencrypt-prod":   `{"spec":{"acme":{"server":"https://acme-v02.api.letsencrypt.org/directory"}}}`,
		"secrets/cloudflare-dns-token":                      "secret/cloudflare-dns-token",
		"objectstores.barmancloud.cnpg.io/tosak-pg-backups": `{"spec":{"configuration":{"destinationPath":"s3://tosak-pg-backups/"}}}`,
	}}
	var out bytes.Buffer
	err := stage.Run(context.Background(), []stage.Stage{c}, h, stage.Options{Apply: true, Out: &out})
	if err == nil {
		t.Fatal("a cluster with every live link passed")
	}
	n := 0
	for _, cut := range RehearsalCuts {
		for _, l := range cut.Links {
			n++
			if !strings.Contains(out.String(), l.What) {
				t.Errorf("not named: %s", l.What)
			}
		}
	}
	if n != 6 || !strings.Contains(err.Error(), "stops here") {
		t.Errorf("%d links in the table; output:\n%s", n, out.String())
	}
}

func TestUntouchedZone(t *testing.T) {
	rec := func(name, addr string) Record { return Record{Name: name, Content: addr} }
	hosts := []string{"a.tosak.net", "b.tosak.net"}
	const live, rehearsal = "198.51.100.1", "203.0.113.9"
	for name, tc := range map[string]struct {
		recs []Record
		want string
	}{
		"live, untouched":         {[]Record{rec("a.tosak.net", live), rec("b.tosak.net", live)}, ""},
		"one points at rehearsal": {[]Record{rec("a.tosak.net", live), rec("b.tosak.net", rehearsal)}, "b.tosak.net: points at the rehearsal"},
		"two addresses":           {[]Record{rec("a.tosak.net", live), rec("b.tosak.net", "198.51.100.2")}, "2 different addresses"},
		"a host without a record": {[]Record{rec("a.tosak.net", live)}, "b.tosak.net: no A record"},
	} {
		got := strings.Join(UntouchedMismatches(tc.recs, hosts, rehearsal), "; ")
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
		if strings.Contains(got, live) {
			t.Errorf("%s: the live address is printed: %s", name, got)
		}
	}
}
