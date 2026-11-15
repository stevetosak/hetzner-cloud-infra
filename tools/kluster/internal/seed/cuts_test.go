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

// Each live link names one object or the whole kind, never both or neither.
func TestEveryLiveLinkNamesOneObjectOrTheKind(t *testing.T) {
	for _, cut := range RehearsalCuts {
		for _, l := range cut.Links {
			if err := l.validate(); err != nil {
				t.Errorf("core/rehearsal/%s: %v", cut.Dir, err)
			}
		}
	}
	for _, bad := range []LiveLink{
		{What: "neither", Kind: "probes.monitoring.coreos.com"},
		{What: "both", Kind: "probes.monitoring.coreos.com", Name: "x", AnyOfKind: true},
	} {
		if bad.validate() == nil {
			t.Errorf("%s: accepted", bad.What)
		}
	}
}

// cluster answers the reads of the rehearsal-cuts Stage from objects keyed
// by kind/name; kind/ answers a read of the whole kind.
type cluster map[string]string

func (c cluster) Run(_ context.Context, cmd string) (string, error) {
	for key, obj := range c {
		kind, name, _ := strings.Cut(key, "/")
		if name == "" {
			name = "--ignore-not-found"
		}
		if strings.Contains(cmd, "'"+kind+"' '"+name+"'") {
			return obj, nil
		}
	}
	return "", nil
}
func (cluster) WriteFile(context.Context, string, string, os.FileMode) error { return nil }
func (cluster) ReadFile(context.Context, string) (string, error)             { return "", nil }

func TestCutsHoldInARehearsal(t *testing.T) {
	c := &Cuts{Bucket: "tosak-pg-backups-rehearsal", Revision: "rehearsal"}
	h := &stage.Host{Name: "cp", Exec: cluster{
		"deployments.apps/argocd-notifications-controller": `{"spec":{"replicas":0}}`,
		"applicationsets.argoproj.io/projects": `{"spec":{"generators":[{"git":{"revision":"rehearsal","directories":[{"path":"projects/mail/*/manifests/overlays/rehearsal"}]}}],` +
			`"template":{"spec":{"source":{"targetRevision":"rehearsal"}}}}}`,
		"clusterissuers.cert-manager.io/letsencrypt-prod":            `{"spec":{"selfSigned":{}}}`,
		"objectstores.barmancloud.cnpg.io/tosak-pg-backups":          `{"spec":{"configuration":{"destinationPath":"s3://tosak-pg-backups-rehearsal/"}}}`,
		"alertmanagers.monitoring.coreos.com/kps-alertmanager":       `{"spec":{"replicas":1}}`,
		"probes.monitoring.coreos.com/":                              `{"items":[{"spec":{"targets":{"staticConfig":{"static":["10.0.4.3:25"]}}}}]}`,
		"objectstores.barmancloud.cnpg.io/tosak-pg-backups-readonly": `{"spec":{"configuration":{"destinationPath":"s3://tosak-pg-backups-rehearsal/"}}}`,
		"cronjobs.batch/restore-drill":                               `{"spec":{"suspend":true,"jobTemplate":{"spec":{"template":{"spec":{"containers":[{"env":[{"name":"REPORT","value":"log"}]}]}}}}}}`,
	}}
	st, err := c.Probe(context.Background(), h)
	if err != nil || !st.Done {
		t.Fatalf("%+v %v", st, err)
	}
}

// The negative test: a cluster as live is, every link in place. The Stage
// names each one and stops the run.
func TestCutsNameEveryLiveLink(t *testing.T) {
	c := &Cuts{Bucket: "tosak-pg-backups-rehearsal", Revision: "rehearsal"}
	h := &stage.Host{Name: "cp", Exec: cluster{
		"deployments.apps/argocd-notifications-controller": `{"spec":{"replicas":1}}`,
		"secrets/argocd-notifications-secret":              "secret/argocd-notifications-secret",
		"applicationsets.argoproj.io/projects": `{"spec":{"generators":[{"git":{"revision":"HEAD","directories":[{"path":"projects/mail/*/manifests/overlays/dev"}]}}],` +
			`"template":{"spec":{"source":{"targetRevision":"HEAD"}}}}}`,
		"clusterissuers.cert-manager.io/letsencrypt-prod":            `{"spec":{"acme":{"server":"https://acme-v02.api.letsencrypt.org/directory"}}}`,
		"secrets/cloudflare-dns-token":                               "secret/cloudflare-dns-token",
		"objectstores.barmancloud.cnpg.io/tosak-pg-backups":          `{"spec":{"configuration":{"destinationPath":"s3://tosak-pg-backups/"}}}`,
		"secrets/alertmanager-receivers":                             "secret/alertmanager-receivers",
		"alertmanagers.monitoring.coreos.com/kps-alertmanager":       `{"spec":{"secrets":["alertmanager-receivers"]}}`,
		"probes.monitoring.coreos.com/":                              `{"items":[{"spec":{"targets":{"staticConfig":{"static":["https://tosak.net/"]}}}}]}`,
		"objectstores.barmancloud.cnpg.io/tosak-pg-backups-readonly": `{"spec":{"configuration":{"destinationPath":"s3://tosak-pg-backups/"}}}`,
		"secrets/telegram":                                           "secret/telegram",
		"secrets/r2-drill-results-credentials":                       "secret/r2-drill-results-credentials",
		"cronjobs.batch/restore-drill":                               `{"spec":{"jobTemplate":{"spec":{"template":{"spec":{"containers":[{"env":[]}]}}}}}}`,
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
	if n != 13 || !strings.Contains(err.Error(), "stops here") {
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

// Each Holds function of the new links fails on the one field that matters.
func TestRehearsalLinkHolds(t *testing.T) {
	c := &Cuts{Revision: "rehearsal"}
	appSet := func(genRev, dir, tmplRev string) []byte {
		return []byte(`{"spec":{"generators":[{"git":{"revision":"` + genRev + `","directories":[{"path":"` + dir + `"}]}}],` +
			`"template":{"spec":{"source":{"targetRevision":"` + tmplRev + `"}}}}}`)
	}
	const reh = "projects/mail/*/manifests/overlays/rehearsal"
	for name, tc := range map[string]struct {
		obj  []byte
		want bool
	}{
		"rehearsal overlays from rehearsal": {appSet("rehearsal", reh, "rehearsal"), true},
		"the generator reads HEAD":          {appSet("HEAD", reh, "rehearsal"), false},
		"the Applications sync HEAD":        {appSet("rehearsal", reh, "HEAD"), false},
		"a dev overlay":                     {appSet("rehearsal", "projects/doma/*/manifests/overlays/dev", "rehearsal"), false},
	} {
		if got, err := rehearsedProjectsOnly(tc.obj, true, c); err != nil || got != tc.want {
			t.Errorf("projects ApplicationSet, %s: %t, %v; want %t", name, got, err, tc.want)
		}
	}

	cron := func(suspend, report string) []byte {
		return []byte(`{"spec":{` + suspend + `"jobTemplate":{"spec":{"template":{"spec":{"containers":[{"env":[` + report + `]}]}}}}}}`)
	}
	for name, tc := range map[string]struct {
		obj  []byte
		want bool
	}{
		"suspended, log":     {cron(`"suspend":true,`, `{"name":"REPORT","value":"log"}`), true},
		"not suspended":      {cron(`"suspend":false,`, `{"name":"REPORT","value":"log"}`), false},
		"no suspend field":   {cron(``, `{"name":"REPORT","value":"log"}`), false},
		"REPORT all":         {cron(`"suspend":true,`, `{"name":"REPORT","value":"all"}`), false},
		"no REPORT variable": {cron(`"suspend":true,`, ``), false},
	} {
		if got, err := suspendedLogOnlyDrill(tc.obj, true, c); err != nil || got != tc.want {
			t.Errorf("drill CronJob, %s: %t, %v; want %t", name, got, err, tc.want)
		}
	}

	for target, want := range map[string]bool{
		"10.0.4.3:25":                            true,
		"https://10.0.4.3/":                      true,
		"127.0.0.1:9115":                         true,
		"blackbox-exporter.monitoring.svc:9115":  true,
		"http://x.monitoring.svc.cluster.local/": true,
		"mail.tosak.net:25":                      false,
		"https://tosak.net/":                     false,
		"46.62.209.249:443":                      false,
		"[2001:db8::1]:25":                       false,
	} {
		if got := insideTarget(target); got != want {
			t.Errorf("insideTarget(%q) = %t, want %t", target, got, want)
		}
	}
	ingress := []byte(`{"items":[{"spec":{"targets":{"ingress":{"selector":{}}}}}]}`)
	if ok, err := insideTargetsOnly(ingress, true, c); err != nil || ok {
		t.Errorf("a Probe that discovers its targets: %t, %v", ok, err)
	}
}
