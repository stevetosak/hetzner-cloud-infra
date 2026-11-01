package coreset

import (
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
)

// repoRoot is the top of the repository, from this package's directory.
const repoRoot = "../../../.."

// repoFS copies the files the check reads out of the real repository, so a
// test can break one of them and leave the rest as they are on the branch.
func repoFS(t *testing.T) fstest.MapFS {
	t.Helper()
	src := os.DirFS(repoRoot)
	m := fstest.MapFS{}
	for _, top := range []string{"core", "projects"} {
		err := fs.WalkDir(src, top, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !(strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".json")) {
				return err
			}
			data, err := fs.ReadFile(src, p)
			m[p] = &fstest.MapFile{Data: data}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{AppsFile, catalog.FileName} {
		data, err := fs.ReadFile(src, f)
		if err != nil {
			t.Fatal(err)
		}
		m[f] = &fstest.MapFile{Data: data}
	}
	return m
}

func edit(t *testing.T, m fstest.MapFS, file, old, new string) {
	t.Helper()
	f, ok := m[file]
	if !ok {
		t.Fatalf("%s is not in the repository", file)
	}
	s := string(f.Data)
	if strings.Count(s, old) != 1 {
		t.Fatalf("%s: %q occurs %d times, want once", file, old, strings.Count(s, old))
	}
	m[file] = &fstest.MapFile{Data: []byte(strings.Replace(s, old, new, 1))}
}

func TestTheRepositoryPasses(t *testing.T) {
	if errs := Check(os.DirFS(repoRoot)); len(errs) > 0 {
		t.Fatalf("the branch fails its own check: %v", errs)
	}
}

func TestListsAreRead(t *testing.T) {
	fsys := os.DirFS(repoRoot)
	core, err := Core(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(core, func(e Element) bool {
		return e == Element{Dir: "cnpg", Path: "core/cnpg", Namespace: "cnpg-system"}
	}) {
		t.Errorf("core list lacks cnpg: %+v", core)
	}
	reh, err := Rehearsal(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(reh, func(e Element) bool { return e.Dir == "pg-drill" }) {
		t.Errorf("the rehearsal list must leave the Restore Drill out: %+v", reh)
	}
	for rehearsal, want := range map[bool]string{false: "HEAD", true: "rehearsal"} {
		if rev, err := TargetRevision(fsys, rehearsal); err != nil || rev != want {
			t.Errorf("TargetRevision(rehearsal %t) = %q, %v; want %q", rehearsal, rev, err, want)
		}
	}
	projects, err := Projects(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(projects, []string{"authos", "doma"}) {
		t.Errorf("projects = %v", projects)
	}
}

// Each case breaks the repository in one place and names the refusal it
// must cause, so a rule that stops firing fails here.
func TestEachRuleFires(t *testing.T) {
	cases := []struct {
		name string
		mut  func(t *testing.T, m fstest.MapFS)
		want string
	}{
		{"a Secret in a build", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, "core/argocd/kustomization.yaml", "  - httproute.yaml\n", "  - httproute.yaml\n  - argocd-notifications-secret.enc.yaml\n")
		}, "a Secret: kluster secrets apply owns it"},
		{"the database Cluster in a build", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, "core/cnpg/kustomization.yaml", "  - objectstore.yaml\n", "  - objectstore.yaml\n  - pg-cluster.yaml\n")
		}, "kluster db create owns it"},
		{"the Cluster's directory in a build", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, "core/cnpg/kustomization.yaml", "  - objectstore.yaml\n", "  - objectstore.yaml\n  - cluster/fresh\n")
		}, "kluster db create owns it"},
		{"a recovery overlay behind the Cluster", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, "core/cnpg/cluster/fresh/pg-cluster.yaml", "serverName: tosak-pg-cluster-g1\n", "serverName: tosak-pg-cluster-g2\n")
		}, "tests serverName"},
		{"CRDs in a build", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, "core/gateway/kustomization.yaml", "  - gateway.yaml\n", "  - gateway.yaml\n  - crds.yaml\n")
		}, "CRDs: the seed owns them"},
		{"a namespace file in a build", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, "core/redis/kustomization.yaml", "  - service.yaml\n", "  - service.yaml\n  - namespace.yaml\n")
		}, "Namespaces: the seed owns them"},
		{"a values file in a build", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, "core/cert-manager/kustomization.yaml", "  - clusterissuer.yaml\n", "  - clusterissuer.yaml\n  - cert-manager-values.yaml\n")
		}, "Helm values"},
		{"a Namespace under another name", func(t *testing.T, m fstest.MapFS) {
			m["core/redis/service.yaml"] = &fstest.MapFile{Data: append(m["core/redis/service.yaml"].Data,
				[]byte("\n---\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: sneaky\n")...)}
		}, "holds Namespace sneaky"},
		{"a missing resource", func(t *testing.T, m fstest.MapFS) {
			delete(m, "core/redis/service.yaml")
		}, "service.yaml, which does not exist"},
		{"a remote resource", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, "core/redis/kustomization.yaml", "  - service.yaml\n", "  - service.yaml\n  - https://example.com/x.yaml\n")
		}, "remote resource"},
		{"an unsynced core kustomization", func(t *testing.T, m fstest.MapFS) {
			m["core/unlisted/kustomization.yaml"] = &fstest.MapFile{Data: []byte("resources: []\n")}
		}, "core/unlisted/kustomization.yaml: no Application syncs it"},
		{"a route without a probe", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, "core/monitoring/probes/public-hosts.yaml", "        - https://doma.tosak.net/api/health/live\n", "")
		}, "projects/doma/httproute.yaml: host doma.tosak.net has no probe"},
		{"a probe without a route", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, "core/monitoring/probes/public-hosts.yaml", "        - https://www.tosak.net/\n", "        - https://www.tosak.net/\n        - https://imaps.tosak.net/\n")
		}, "a probe targets imaps.tosak.net, which no synced HTTPRoute serves"},
		{"a probe that is not an https URL", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, "core/monitoring/probes/public-hosts.yaml", "        - https://www.tosak.net/\n", "        - www.tosak.net\n")
		}, `target "www.tosak.net" is not an https URL`},
		{"a probe that discovers its targets", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, "core/monitoring/probes/public-hosts.yaml", "    staticConfig:\n      static:\n        - https://tosak.net/\n        - https://www.tosak.net/\n",
				"    ingress:\n      selector: {}\n")
		}, "list the targets in staticConfig.static"},
		{"a core namespace outside the AppProject", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, AppProjectsFile, "    - {server: https://kubernetes.default.svc, namespace: cnpg-system}\n", "")
		}, "namespace cnpg-system of cnpg is not a destination"},
		{"a rehearsal element not in the main list", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, RehearsalArgoCDFile, "          - {dir: redis, path: core/redis, namespace: redis}\n",
				"          - {dir: redis, path: core/redis, namespace: redis}\n          - {dir: extra, path: core/extra, namespace: extra}\n")
		}, "dir extra is not in the main list"},
		{"a rehearsal element pointing elsewhere", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, RehearsalArgoCDFile, "path: core/rehearsal/cert-manager,", "path: core/gateway,")
		}, "dir cert-manager syncs core/gateway"},
		{"a project without an AppProject", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, AppProjectsFile, "  name: doma\n", "  name: doma-old\n")
		}, `no AppProject "doma"`},
		{"a project without a namespace file", func(t *testing.T, m fstest.MapFS) {
			delete(m, "projects/doma/namespace.yaml")
		}, "projects/doma/namespace.yaml"},
		{"a project missing from a listener", func(t *testing.T, m fstest.MapFS) {
			// The first listener only: the check reads each one.
			s := string(m[GatewayFile].Data)
			m[GatewayFile] = &fstest.MapFile{Data: []byte(strings.Replace(s,
				"values: [gateway, argocd, authos, doma]", "values: [gateway, argocd, authos]", 1))}
		}, "does not admit routes from doma"},
		{"a component missing from the deploy catalog", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, AppsFile, `"doma-web"`, `"doma"`)
		}, "no entry doma-web"},
		{"a reloaded project outside Reloader's scope", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, ReloaderFile, "--namespaces=authos,doma,redis,reloader", "--namespaces=authos,redis,reloader")
		}, "--namespaces does not name doma"},
		{"a reloaded project outside the core AppProject", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, AppProjectsFile, "    - {server: https://kubernetes.default.svc, namespace: doma}\n", "")
		}, "doma has a Deployment Reloader restarts, but is not a destination"},
		{"an element namespace no namespace file makes", func(t *testing.T, m fstest.MapFS) {
			delete(m, "core/redis/namespace.yaml")
		}, "namespace redis of core-redis is in no namespace file"},
		{"a rehearsal that syncs master", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, RehearsalArgoCDFile, "        path: /spec/template/spec/source/targetRevision\n", "        path: /spec/template/spec/source/repoURL\n")
		}, "no replace of /spec/template/spec/source/targetRevision"},
		{"an issued key a rehearsal would make fresh", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, catalog.FileName, "    file: core/cert-manager/cloudflare-dns-token.enc.yaml\n    rehearsal: omit\n",
				"    file: core/cert-manager/cloudflare-dns-token.enc.yaml\n")
		}, "cert-manager/cloudflare-dns-token.api-token is issued"},
		{"a fresh copy of an omitted Secret", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, catalog.FileName, "        kind: literal\n        value: tosak\n",
				"        kind: copy\n        from: argocd/argocd-notifications-secret.github-token\n")
		}, "copies argocd/argocd-notifications-secret.github-token, which a rehearsal does not make"},
		{"an issued key without its rehearsal item", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, catalog.FileName, "        field: access_key_id\n        rehearsalItem: kluster/r2-backup-rehearsal-ro\n",
				"        field: access_key_id\n")
		}, "pg-cluster/r2-source-credentials.ACCESS_KEY_ID is issued"},
		{"an omit no rehearsal needs", func(t *testing.T, m fstest.MapFS) {
			edit(t, m, catalog.FileName, "    file: core/pg-drill/telegram.enc.yaml\n", "    file: core/pg-drill/telegram.enc.yaml\n    rehearsal: omit\n")
		}, "pg-drill/telegram has rehearsal: omit, but no rehearsal namespace holds it"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := repoFS(t)
			c.mut(t, m)
			errs := Check(m)
			for _, e := range errs {
				if strings.Contains(e.Error(), c.want) {
					return
				}
			}
			t.Fatalf("want a refusal containing %q, got %v", c.want, errs)
		})
	}
}
