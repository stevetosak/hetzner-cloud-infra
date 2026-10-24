package kbuild

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

// The pin is what makes the seed's build ArgoCD's build: a go get that
// moves it must fail here. A test binary carries no module versions, so the
// test reads go.mod.
func TestKustomizeIsArgoCDs(t *testing.T) {
	data, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	f, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"sigs.k8s.io/kustomize/api": APIVersion, "sigs.k8s.io/kustomize/kyaml": KYAMLVersion}
	for _, r := range f.Require {
		if v, ok := want[r.Mod.Path]; ok {
			if r.Mod.Version != v {
				t.Errorf("%s is at %s; kustomize v5.8.1, which ArgoCD v3.5.3 ships, uses %s", r.Mod.Path, r.Mod.Version, v)
			}
			delete(want, r.Mod.Path)
		}
	}
	if len(want) > 0 {
		t.Errorf("go.mod does not require %v", want)
	}
}

func TestTrackMatchesArgoCD(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("kustomization.yaml", "resources: [a.yaml]\n")
	write("a.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: argocd
  annotations:
    argocd.argoproj.io/installation-id: stale
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: reader
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: plain
`)
	out, err := YAML(dir, "core-argocd", "argocd")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"argocd.argoproj.io/tracking-id: core-argocd:apps/Deployment:argocd/web",
		// A cluster-scoped resource takes the destination namespace.
		"argocd.argoproj.io/tracking-id: core-argocd:rbac.authorization.k8s.io/ClusterRole:argocd/reader",
		// The core group is empty.
		"argocd.argoproj.io/tracking-id: core-argocd:/ConfigMap:argocd/plain",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("build lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, InstallationIDAnnotation) {
		t.Errorf("an installation ID survived:\n%s", s)
	}
}

// Every synced directory and rehearsal cut builds, as CI proves with kubectl.
func TestRepositoryBuilds(t *testing.T) {
	dirs, err := filepath.Glob("../../../../core/*/kustomization.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cuts, _ := filepath.Glob("../../../../core/rehearsal/*/kustomization.yaml")
	for _, k := range append(dirs, cuts...) {
		if _, err := Build(filepath.Dir(k)); err != nil {
			t.Error(err)
		}
	}
	if len(dirs) == 0 {
		t.Fatal("no core kustomization found")
	}
}
