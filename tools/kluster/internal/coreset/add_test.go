package coreset

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// newProject is a project the repository never registers.
var newProject = AddRequest{
	Project:   "example",
	Component: "web",
	Repo:      "stevetosak/example",
	TagPrefix: "web-v",
	Host:      "example.tosak.net",
}

// register runs AddProject and writes its edits into m.
func register(t *testing.T, m fstest.MapFS, req AddRequest) Registration {
	t.Helper()
	reg, err := AddProject(m, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range reg.Edits {
		m[e.Path] = &fstest.MapFile{Data: e.New}
	}
	return reg
}

func edited(reg Registration) []string {
	var out []string
	for _, e := range reg.Edits {
		out = append(out, e.Path)
	}
	return out
}

func TestAddProjectRegistersEverywhereTheCheckAsks(t *testing.T) {
	m := repoFS(t)
	before := fstest.MapFS{}
	for k, v := range m {
		before[k] = v
	}
	reg := register(t, m, newProject)

	if errs := Check(m); len(errs) > 0 {
		t.Fatalf("the registered repository fails the check: %v", errs)
	}
	projects, err := Projects(m)
	if err != nil || !slices.Equal(projects, []string{"authos", "doma", "tosak-net", "mail", "example"}) {
		t.Fatalf("projects = %v, %v", projects, err)
	}
	want := []string{ProjectsAppSetFile, AppProjectsFile, GatewayFile, LimitRangesDir + "/kustomization.yaml",
		LimitRangesDir + "/example/kustomization.yaml", "projects/example/namespace.yaml", AppsFile}
	if got := edited(reg); !slices.Equal(got, want) {
		t.Errorf("edited %v, want %v", got, want)
	}
	newFiles := []string{LimitRangesDir + "/example/kustomization.yaml", "projects/example/namespace.yaml"}
	for _, e := range reg.Edits {
		if (e.Old == nil) != slices.Contains(newFiles, e.Path) {
			t.Errorf("%s: new file %t", e.Path, e.Old == nil)
		}
	}

	// The YAML edits only add lines: every comment and line stays, in order.
	for _, f := range []string{ProjectsAppSetFile, AppProjectsFile, LimitRangesDir + "/kustomization.yaml"} {
		if !isSubsequence(lines(before[f].Data), lines(m[f].Data)) {
			t.Errorf("%s lost or changed a line", f)
		}
	}
	for _, s := range []struct{ file, line string }{
		{ProjectsAppSetFile, "          - path: projects/mail/*/manifests/overlays/dev\n          - path: projects/example/*/manifests/overlays/dev\n"},
		{AppProjectsFile, "    - {server: https://kubernetes.default.svc, namespace: mail}\n    - {server: https://kubernetes.default.svc, namespace: example}\n"},
		{AppProjectsFile, "      kind: '*'\n---\napiVersion: argoproj.io/v1alpha1\nkind: AppProject\nmetadata:\n  name: example\n"},
		{AppProjectsFile, "      kind: '*'\n---\n# THE CORE PROJECT"},
		{LimitRangesDir + "/kustomization.yaml", "  - mail\n  - example\n"},
		{AppsFile, `  "mail-stalwart": { "repo": null, "tagPrefix": "v" },` + "\n" +
			`  "example-web": { "repo": "stevetosak/example", "tagPrefix": "web-v", "host": "example.tosak.net" }` + "\n}"},
	} {
		if !strings.Contains(string(m[s.file].Data), s.line) {
			t.Errorf("%s lacks %q", s.file, s.line)
		}
	}
	if n := strings.Count(string(m[GatewayFile].Data), "values: [gateway, argocd, authos, doma, tosak-net, mail, example, monitoring]\n"); n != 2 {
		t.Errorf("%s: %d listeners admit example, want 2", GatewayFile, n)
	}
	if n := strings.Count(string(m[AppProjectsFile].Data), "# THE CORE PROJECT"); n != 1 {
		t.Errorf("%s: the core comment occurs %d times", AppProjectsFile, n)
	}
}

func TestAddProjectTwiceChangesNothing(t *testing.T) {
	m := repoFS(t)
	register(t, m, newProject)
	if reg := register(t, m, newProject); len(reg.Edits) > 0 {
		t.Fatalf("a second run edits %v", edited(reg))
	}
}

func TestAddProjectFillsOnlyWhatIsMissing(t *testing.T) {
	m := repoFS(t)
	register(t, m, newProject)
	edit(t, m, LimitRangesDir+"/kustomization.yaml", "  - example\n", "")
	delete(m, "projects/example/namespace.yaml")

	reg := register(t, m, newProject)
	want := []string{LimitRangesDir + "/kustomization.yaml", "projects/example/namespace.yaml"}
	if got := edited(reg); !slices.Equal(got, want) {
		t.Fatalf("edited %v, want %v", got, want)
	}
	if errs := Check(m); len(errs) > 0 {
		t.Fatalf("the repaired repository fails the check: %v", errs)
	}
}

func TestAddProjectPutsARowAfterTheProjectsRows(t *testing.T) {
	m := repoFS(t)
	reg := register(t, m, AddRequest{Project: "authos", Component: "worker", Repo: "stevetosak/authos", TagPrefix: "authos-worker-v"})
	if got := edited(reg); !slices.Equal(got, []string{AppsFile}) {
		t.Fatalf("a registered project edits %v, want only the catalog", got)
	}
	s := string(m[AppsFile].Data)
	if !strings.Contains(s, `"host": "authos-demo.tosak.net/duster" },`+"\n"+`  "authos-worker": { "repo": "stevetosak/authos", "tagPrefix": "authos-worker-v" },`+"\n") {
		t.Errorf("the row is not after authos-duster:\n%s", s)
	}
	var rows map[string]json.RawMessage
	if err := json.Unmarshal(m[AppsFile].Data, &rows); err != nil || len(rows) != 11 {
		t.Errorf("catalog: %d rows, %v", len(rows), err)
	}
}

func TestAddProjectRefusesABadName(t *testing.T) {
	for name, want := range map[string]string{
		"Tosak_Net":   "not a namespace name",
		"monitoring":  "a core directory already writes",
		"kube-system": "Kubernetes owns",
		"default":     "Kubernetes owns",
	} {
		_, err := AddProject(repoFS(t), AddRequest{Project: name})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err %v, want %q", name, err, want)
		}
	}
}

func TestAddProjectRefusesAResultTheCheckRefuses(t *testing.T) {
	cases := []struct {
		name string
		mut  func(m fstest.MapFS)
		req  AddRequest
		want string
	}{
		{"a route without a probe", func(m fstest.MapFS) {
			m["projects/example/httproute.yaml"] = &fstest.MapFile{Data: []byte(
				"apiVersion: gateway.networking.k8s.io/v1\nkind: HTTPRoute\nmetadata:\n  name: site\nspec:\n  hostnames: [new.tosak.net]\n")}
		}, newProject, "host new.tosak.net has no probe"},
		{"an overlay without its catalog row", func(m fstest.MapFS) {
			m["projects/example/web/manifests/overlays/dev/kustomization.yaml"] = &fstest.MapFile{Data: []byte("resources: []\n")}
		}, AddRequest{Project: "example"}, "no entry example-web"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := repoFS(t)
			c.mut(m)
			_, err := AddProject(m, c.req)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %q", err, c.want)
			}
		})
	}
}

func TestAddProjectLeavesStandingRefusalsStanding(t *testing.T) {
	m := repoFS(t)
	edit(t, m, "core/monitoring/probes/public-hosts.yaml", "        - https://doma.tosak.net/api/health/live\n", "")
	reg := register(t, m, newProject)
	if len(reg.Standing) != 1 || !strings.Contains(reg.Standing[0].Error(), "doma.tosak.net has no probe") {
		t.Fatalf("standing = %v", reg.Standing)
	}
}

func lines(b []byte) []string { return strings.Split(string(b), "\n") }

// isSubsequence reports whether every line of a is in b, in order.
func isSubsequence(a, b []string) bool {
	i := 0
	for _, l := range b {
		if i < len(a) && a[i] == l {
			i++
		}
	}
	return i == len(a)
}
