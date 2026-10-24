// Package kbuild builds a kustomization the way ArgoCD's repo-server does:
// kustomize v5.8.1, the version ArgoCD v3.5.3 ships (hack/tool-versions.sh),
// with its default options, and the tracking annotation ArgoCD's controller
// adds before it applies. The seed applies what ArgoCD would sync, under
// ArgoCD's own field manager, so the two must produce the same object: a
// field the seed left out would be removed from ArgoCD's ownership on every
// seed, and put back by the next sync.
package kbuild

import (
	"fmt"

	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// The module versions of kustomize v5.8.1 (its go.mod). go.mod pins them;
// a test refuses another.
const (
	APIVersion   = "v0.21.1"
	KYAMLVersion = "v0.21.1"
)

// The annotations ArgoCD v3.5.3 tracks a resource by (common/common.go).
const (
	TrackingAnnotation       = "argocd.argoproj.io/tracking-id"
	InstallationIDAnnotation = "argocd.argoproj.io/installation-id"
)

// Build runs kustomize on dir, a directory on the laptop's disk.
func Build(dir string) (resmap.ResMap, error) {
	k := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	m, err := k.Run(filesys.MakeFsOnDisk(), dir)
	if err != nil {
		return nil, fmt.Errorf("kustomize build %s: %w", dir, err)
	}
	return m, nil
}

// Track adds what ArgoCD's controller adds to each resource of the
// Application app, whose destination namespace is ns, before it applies:
// the tracking annotation <app>:<group>/<kind>:<namespace>/<name>, where a
// resource without a namespace (a cluster-scoped one, too) takes ns
// (util/argo/resource_tracking.go, UnstructuredToAppInstanceValue). This
// install sets no installation ID, so that annotation stays off.
func Track(m resmap.ResMap, app, ns string) error {
	for _, r := range m.Resources() {
		gvk := r.GetGvk()
		rns := r.GetNamespace()
		if rns == "" {
			rns = ns
		}
		a := r.GetAnnotations()
		if a == nil {
			a = map[string]string{}
		}
		delete(a, InstallationIDAnnotation)
		a[TrackingAnnotation] = fmt.Sprintf("%s:%s/%s:%s/%s", app, gvk.Group, gvk.Kind, rns, r.GetName())
		if err := r.SetAnnotations(a); err != nil {
			return fmt.Errorf("%s: %w", r.CurId(), err)
		}
	}
	return nil
}

// YAML builds dir, tracks it as app in ns, and returns the documents.
func YAML(dir, app, ns string) ([]byte, error) {
	m, err := Build(dir)
	if err != nil {
		return nil, err
	}
	if err := Track(m, app, ns); err != nil {
		return nil, err
	}
	return m.AsYaml()
}
