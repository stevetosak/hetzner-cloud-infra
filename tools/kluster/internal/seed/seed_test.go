package seed

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/coreset"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kbuild"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

const repoRoot = "../../../.."

func repoCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.Load(filepath.Join(repoRoot, catalog.FileName))
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// Both trees are the repository here: the build is what matters, not the
// revision.
func TestBuildReadsTheRepository(t *testing.T) {
	for _, tc := range []struct {
		rehearsal bool
		hosts     []string
		secrets   []string
		cuts      []string
	}{
		{false, []string{"argocd.tosak.net", "authos-api.tosak.net", "authos-demo.tosak.net", "authos.tosak.net", "doma.tosak.net", "grafana.tosak.net", "tosak.net", "www.tosak.net"}, nil, nil},
		{true, []string{"argocd.tosak.net", "grafana.tosak.net", "tosak.net", "www.tosak.net"}, []string{"monitoring/grafana-admin", "pg-cluster/db-credentials", "pg-cluster/r2-backup-credentials", "pg-cluster/r2-source-credentials", "redis/redis"},
			[]string{"rehearsal-cuts"}},
	} {
		p, err := Build(Inputs{Rehearsal: tc.rehearsal, WorkTree: repoRoot, Synced: repoRoot, Catalog: repoCatalog(t), RehearsalBucket: "tosak-pg-backups-rehearsal"})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, s := range p.Stages {
			names = append(names, s.Name())
		}
		want := append(append([]string{"seed-crds", "seed-namespaces", "seed-secrets", "seed-argocd", "core-healthy"}, tc.cuts...), "lb-address", "zone")
		if !slices.Equal(names, want) {
			t.Errorf("stages %v", names)
		}
		if b := p.BeforeZone(); len(b) != len(want)-1 || b[len(b)-1].Name() != "lb-address" {
			t.Errorf("BeforeZone %d stages, ends with %s", len(b), b[len(b)-1].Name())
		}
		if (p.Monitoring != nil) != tc.rehearsal {
			t.Errorf("rehearsal %t: monitoring-clean planned: %t", tc.rehearsal, p.Monitoring != nil)
		}
		if !slices.Equal(p.Hosts, tc.hosts) {
			t.Errorf("rehearsal %t: hosts %v, want %v", tc.rehearsal, p.Hosts, tc.hosts)
		}
		sec := p.Stages[2].(*Secrets)
		var refs []string
		for _, s := range sec.Secrets {
			refs = append(refs, s.Ref.String())
		}
		if tc.rehearsal && !slices.Equal(refs, tc.secrets) {
			t.Errorf("rehearsal Secrets %v, want %v", refs, tc.secrets)
		}
		if !tc.rehearsal && len(refs) != len(repoCatalog(t).Secrets) {
			t.Errorf("live seeds %d of %d Secrets", len(refs), len(repoCatalog(t).Secrets))
		}
		argocd := p.Stages[3].(*Apply)
		if argocd.Target != (Target{Manager: ManagerArgoCD}) {
			t.Errorf("seed-argocd target %+v", argocd.Target)
		}
		if !bytes.Contains(argocd.Manifest, []byte("argocd.argoproj.io/tracking-id: core-argocd:apps/Deployment:argocd/argocd-server")) {
			t.Error("the argocd build is not tracked as core-argocd")
		}
		hasProjects := bytes.Contains(argocd.Manifest, []byte("name: projects\n"))
		if hasProjects == tc.rehearsal {
			t.Errorf("rehearsal %t: the projects ApplicationSet in the build is %t", tc.rehearsal, hasProjects)
		}
		healthy := p.Stages[4].(*Healthy)
		if slices.Contains(healthy.Applications, "core-pg-drill") == tc.rehearsal {
			t.Errorf("rehearsal %t: Applications %v", tc.rehearsal, healthy.Applications)
		}
	}
}

// Found in the rehearsal of 2026-10-24: core/reloader holds Roles in the
// projects' namespaces, which a rehearsal does not make, so core-reloader
// retried its sync for ever. Its rehearsal cut drops them; the uncut build
// is refused.
func TestBuildRefusesAnObjectOutsideTheNamespaces(t *testing.T) {
	ws, err := coreset.SeedFor(os.DirFS(repoRoot), true)
	if err != nil {
		t.Fatal(err)
	}
	namespaces, err := ws.Namespaces(os.DirFS(repoRoot))
	if err != nil {
		t.Fatal(err)
	}
	for dir, refused := range map[string]bool{"core/reloader": true, "core/rehearsal/reloader": false} {
		m, err := kbuild.YAML(filepath.Join(repoRoot, dir), "core-reloader", "reloader")
		if err != nil {
			t.Fatal(err)
		}
		err = inNamespaces("core-reloader", m, namespaces)
		if (err != nil) != refused {
			t.Errorf("%s in a rehearsal: %v, want refused %t", dir, err, refused)
		}
		if refused && err != nil && !strings.Contains(err.Error(), "Role authos/reloader-reloader-role") {
			t.Errorf("%s: %v does not name the object", dir, err)
		}
	}
}

// fakeCP is a Control Plane: it answers the kubectl commands the Stages
// run, from state the test sets.
type fakeCP struct {
	files map[string]string
	// diffRC and diffOut answer kubectl diff.
	diffRC  int
	diffOut string
	applied []string
	secrets map[string]string // "<ns>/<name>" → YAML
	apps    string
	addrs   string
	gets    int
}

func (f *fakeCP) Run(_ context.Context, cmd string) (string, error) {
	switch {
	case strings.HasPrefix(cmd, "mkdir"):
		return "", nil
	case strings.Contains(cmd, "kubectl diff"):
		return fmt.Sprintf("%skluster-rc=%d", f.diffOut, f.diffRC), nil
	case strings.Contains(cmd, "kubectl apply"):
		file := regexp.MustCompile(`-f (\S+)`).FindStringSubmatch(cmd)[1]
		f.applied = append(f.applied, file)
		if strings.HasPrefix(file, "/run/kluster/") {
			var m struct {
				Metadata struct{ Name, Namespace string }
			}
			if err := json.Unmarshal(yamlToJSON(f.files[file]), &m); err != nil {
				return "", err
			}
			f.secrets[m.Metadata.Namespace+"/"+m.Metadata.Name] = f.files[file]
		} else {
			f.diffRC, f.diffOut = 0, ""
		}
		return "", nil
	case strings.Contains(cmd, "kubectl get secret"):
		m := regexp.MustCompile(`-n '([^']+)' '([^']+)'`).FindStringSubmatch(cmd)
		return f.secrets[m[1]+"/"+m[2]], nil
	case strings.Contains(cmd, "get applications"):
		f.gets++
		return f.apps, nil
	case strings.Contains(cmd, "get gateway"):
		return f.addrs, nil
	}
	return "", fmt.Errorf("unexpected command %q", cmd)
}

func (f *fakeCP) WriteFile(_ context.Context, path, content string, _ os.FileMode) error {
	if f.files == nil {
		f.files = map[string]string{}
	}
	f.files[path] = content
	return nil
}

func (f *fakeCP) ReadFile(_ context.Context, path string) (string, error) { return f.files[path], nil }

// yamlToJSON reads the two metadata fields a Secret manifest starts with.
func yamlToJSON(y string) []byte {
	name := regexp.MustCompile(`(?m)^  name: (\S+)`).FindStringSubmatch(y)
	ns := regexp.MustCompile(`(?m)^  namespace: (\S+)`).FindStringSubmatch(y)
	return fmt.Appendf(nil, `{"Metadata":{"Name":%q,"Namespace":%q}}`, name[1], ns[1])
}

func TestApplyIsDoneWhenTheDiffIsEmpty(t *testing.T) {
	cp := &fakeCP{diffRC: 1, diffOut: "diff -u -N /tmp/LIVE-1/apiextensions.k8s.io.v1.CustomResourceDefinition..applications.argoproj.io /tmp/MERGED-1/x\n+ spec\n"}
	h := &stage.Host{Name: "k8s-cp", Exec: cp}
	a := &Apply{StageName: "seed-crds", Manifest: []byte("kind: CustomResourceDefinition\n"), Target: Target{Manager: ManagerSeed}, What: "CRDs"}
	var out bytes.Buffer
	if err := stage.Run(context.Background(), []stage.Stage{a}, h, stage.Options{Out: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "apiextensions.k8s.io.v1.CustomResourceDefinition..applications.argoproj.io") || len(cp.applied) != 0 {
		t.Errorf("Plan Mode: applied %v, printed:\n%s", cp.applied, out.String())
	}
	if err := stage.Run(context.Background(), []stage.Stage{a}, h, stage.Options{Out: &out, Apply: true}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cp.applied, []string{workDir + "/seed-crds.yaml"}) {
		t.Errorf("applied %v", cp.applied)
	}

	cp.diffRC, cp.diffOut = 2, "error: no matches for kind\n"
	if st, err := a.Probe(context.Background(), h); err != nil || st.Done || !strings.Contains(st.Detail, "exit 2") {
		t.Errorf("a diff before the CRDs exist: %+v, %v", st, err)
	}
}

func TestRehearsalSecretsAreMadeOnce(t *testing.T) {
	p, err := Build(Inputs{Rehearsal: true, WorkTree: repoRoot, Synced: repoRoot, Catalog: repoCatalog(t), RehearsalBucket: "b"})
	if err != nil {
		t.Fatal(err)
	}
	s := p.Stages[2].(*Secrets)
	s.Vault = rehearsalItems{}
	cp := &fakeCP{secrets: map[string]string{}}
	h := &stage.Host{Name: "k8s-cp", Exec: cp}
	var out bytes.Buffer
	if err := stage.Run(context.Background(), []stage.Stage{s}, h, stage.Options{Out: &out, Apply: true}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(cp.secrets) != 5 {
		t.Fatalf("made %v", cp.secrets)
	}
	first := cp.secrets["redis/redis"]
	if err := stage.Run(context.Background(), []stage.Stage{s}, h, stage.Options{Out: &out, Apply: true}); err != nil {
		t.Fatal(err)
	}
	if cp.secrets["redis/redis"] != first || len(cp.applied) != 5 {
		t.Error("a second seed made a Secret again")
	}
	if strings.Contains(out.String(), "data:") {
		t.Errorf("output holds a manifest:\n%s", out.String())
	}

	cp.secrets["redis/redis"] = "apiVersion: v1\nkind: Secret\nmetadata: {name: redis, namespace: redis}\ndata: {other: eA==}\n"
	if _, err := s.Probe(context.Background(), h); err == nil || !strings.Contains(err.Error(), "delete it") {
		t.Errorf("a Secret without its key: %v", err)
	}
}

// rehearsalItems answers only a rehearsal item: a read of a live item is a
// fault the test must see.
type rehearsalItems struct{}

func (rehearsalItems) Issued(_ context.Context, k *catalog.Key) ([]byte, error) {
	if !strings.Contains(k.Item, "rehearsal") {
		return nil, fmt.Errorf("read the live item %s", k.Item)
	}
	return []byte("x-" + k.Field), nil
}

func TestHealthyWaitsThenNamesWhatIsNot(t *testing.T) {
	cp := &fakeCP{apps: `{"items":[
		{"metadata":{"name":"core-redis"},"status":{"sync":{"status":"Synced"},"health":{"status":"Healthy"}}},
		{"metadata":{"name":"core-cnpg"},"status":{"sync":{"status":"OutOfSync"},"health":{"status":"Progressing"}}}]}`}
	h := &stage.Host{Name: "k8s-cp", Exec: cp}
	var slept time.Duration
	c := &Healthy{Applications: []string{"core-redis", "core-cnpg", "core-argocd"},
		Waiting: Waiting{Timeout: time.Minute, Poll: 10 * time.Second, Sleep: func(d time.Duration) { slept += d }}}
	err := c.Act(context.Background(), h)
	if err == nil || !strings.Contains(err.Error(), "core-cnpg: OutOfSync/Progressing") || !strings.Contains(err.Error(), "core-argocd: missing") {
		t.Fatalf("timeout: %v", err)
	}
	if slept != time.Minute || cp.gets != 7 {
		t.Errorf("slept %s over %d reads", slept, cp.gets)
	}
}

func TestPublicIPv4(t *testing.T) {
	ip, err := publicIPv4([]string{"77.42.14.48", "2a01:4f9:c01e:39a::1", "10.0.4.2"})
	if err != nil || ip != "77.42.14.48" {
		t.Errorf("live's addresses: %q, %v", ip, err)
	}
	if ip, _ := publicIPv4([]string{"10.0.4.2"}); ip != "" {
		t.Errorf("a private address only: %q", ip)
	}
	if _, err := publicIPv4([]string{"77.42.14.48", "77.42.14.49"}); err == nil {
		t.Error("two public addresses passed")
	}
}

// cloudflare serves two pages of A records, and refuses a request without
// the token.
func cloudflare(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer read-token" {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`)
			return
		}
		switch {
		case r.URL.Path == "/zones" && r.URL.Query().Get("name") == "tosak.net":
			fmt.Fprint(w, `{"success":true,"result":[{"id":"z1"}]}`)
		case r.URL.Path == "/zones/z1/dns_records" && r.URL.Query().Get("type") == "A":
			if r.URL.Query().Get("page") == "1" {
				fmt.Fprint(w, `{"success":true,"result":[{"name":"doma.tosak.net","content":"77.42.14.48","proxied":true}],"result_info":{"page":1,"total_pages":2}}`)
				return
			}
			fmt.Fprint(w, `{"success":true,"result":[{"name":"argocd.tosak.net","content":"198.51.100.7","proxied":true}],"result_info":{"page":2,"total_pages":2}}`)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestZoneNamesTheChangeAndRefuses(t *testing.T) {
	srv := cloudflare(t)
	defer srv.Close()
	addr := "77.42.14.48"
	z := &Zone{Reader: &Cloudflare{Zone: "tosak.net", Token: "read-token", BaseURL: srv.URL},
		Hosts: []string{"argocd.tosak.net", "authos.tosak.net", "doma.tosak.net"}, Address: &addr}
	var out bytes.Buffer
	err := stage.Run(context.Background(), []stage.Stage{z}, &stage.Host{Name: "zone"}, stage.Options{Out: &out, Apply: true})
	if err == nil {
		t.Fatal("a wrong zone passed")
	}
	for _, want := range []string{
		"argocd.tosak.net: A 198.51.100.7; change it to 77.42.14.48",
		"authos.tosak.net: no A record; add one for 77.42.14.48",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "doma.tosak.net:") {
		t.Errorf("a right record is reported:\n%s", out.String())
	}

	bad := &Cloudflare{Zone: "tosak.net", Token: "wrong", BaseURL: srv.URL}
	if _, err := bad.ARecords(context.Background()); err == nil || !strings.Contains(err.Error(), "10000") {
		t.Errorf("a refused token: %v", err)
	}
}

func TestExportWritesTheFetchedCommit(t *testing.T) {
	var ar bytes.Buffer
	tw := tar.NewWriter(&ar)
	for name, body := range map[string]string{"core/argocd/kustomization.yaml": "resources: []\n"} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	var ran []string
	git := func(_ context.Context, args ...string) ([]byte, error) {
		ran = append(ran, strings.Join(args, " "))
		switch args[0] {
		case "rev-parse":
			return []byte("abc123\n"), nil
		case "archive":
			return ar.Bytes(), nil
		}
		return nil, nil
	}
	dst := t.TempDir()
	commit, err := Export(context.Background(), git, "rehearsal", dst)
	if err != nil || commit != "abc123" {
		t.Fatalf("Export = %q, %v", commit, err)
	}
	if !slices.Equal(ran, []string{"fetch --quiet origin rehearsal", "rev-parse --verify FETCH_HEAD^{commit}", "archive --format=tar abc123"}) {
		t.Errorf("ran %v", ran)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "core/argocd/kustomization.yaml")); err != nil || string(b) != "resources: []\n" {
		t.Errorf("exported %q, %v", b, err)
	}

	var evil bytes.Buffer
	tw = tar.NewWriter(&evil)
	tw.WriteHeader(&tar.Header{Name: "../outside", Mode: 0o644, Typeflag: tar.TypeReg})
	tw.Close()
	if err := untar(&evil, t.TempDir()); err == nil {
		t.Error("an entry outside the export passed")
	}
}
