package seed

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// The field managers the seed applies as.
const (
	// ManagerSeed owns what only the seed applies: CRDs and Namespaces.
	ManagerSeed = kube.FieldManager
	// ManagerArgoCD is the manager ArgoCD v3.5.3 applies as server-side
	// (common/common.go:128), always with force
	// (gitops-engine resource_ops.go:486). The seed installs ArgoCD under it,
	// so a later sync owns the same fields and a repair seed always wins.
	ManagerArgoCD = "argocd-controller"
)

// workDir holds the manifests the seed applies on the Control Plane. None
// holds a value: Secrets go through kube.SecretDir.
const workDir = "/var/lib/kluster/seed"

const adminEnv = "KUBECONFIG=" + kube.AdminConf + " "

// Target is how one manifest goes to the cluster. It passes no -n: kubectl
// then refuses every object of another namespace, and a build spans several
// (core-cnpg writes into pg-cluster, core-reloader into each project). Every
// namespaced object of a core build names its namespace; only cluster-scoped
// ones (ClusterIssuer, GatewayClass) name none.
type Target struct {
	Manager string
}

func (t Target) flags() string {
	return "--server-side --force-conflicts --field-manager=" + t.Manager
}

// put writes manifest to the Control Plane for one kubectl call.
func put(ctx context.Context, h *stage.Host, name string, manifest []byte) (string, error) {
	if _, err := h.Exec.Run(ctx, "mkdir -p -m 700 "+workDir); err != nil {
		return "", err
	}
	p := workDir + "/" + name + ".yaml"
	return p, h.Exec.WriteFile(ctx, p, string(manifest), 0o600)
}

// Diff is what `kubectl diff --server-side` reports for one manifest.
type Diff struct {
	// Objects are the objects an apply would change, as kubectl names
	// them: <group>.<version>.<Kind>.<namespace>.<name>.
	Objects []string
	Text    string
}

// Empty reports whether an apply would change nothing.
func (d Diff) Empty() bool { return len(d.Objects) == 0 }

// Summary names the objects that differ, one per line.
func (d Diff) Summary() string {
	if d.Empty() {
		return "no change\n"
	}
	return fmt.Sprintf("%d object(s) differ:\n  %s\n", len(d.Objects), strings.Join(d.Objects, "\n  "))
}

const rcMark = "kluster-rc="

// diff runs kubectl diff on the Control Plane. Exit 1 means a difference;
// anything above it is an error (kubectl diff's contract).
func diff(ctx context.Context, h *stage.Host, name string, manifest []byte, t Target) (Diff, error) {
	p, err := put(ctx, h, name, manifest)
	if err != nil {
		return Diff{}, err
	}
	out, err := h.Exec.Run(ctx, adminEnv+"kubectl diff "+t.flags()+" -f "+p+" 2>&1; echo "+rcMark+"$?")
	if err != nil {
		return Diff{}, err
	}
	i := strings.LastIndex(out, rcMark)
	if i < 0 {
		return Diff{}, fmt.Errorf("kubectl diff %s: no exit code in its output", name)
	}
	rc, err := strconv.Atoi(strings.TrimSpace(out[i+len(rcMark):]))
	if err != nil {
		return Diff{}, fmt.Errorf("kubectl diff %s: %w", name, err)
	}
	text := out[:i]
	switch rc {
	case 0:
		return Diff{}, nil
	case 1:
		return Diff{Objects: changedObjects(text), Text: text}, nil
	}
	return Diff{}, fmt.Errorf("kubectl diff %s: exit %d: %s", name, rc, head(text, 20))
}

// changedObjects reads the object names off kubectl diff's header lines,
// `diff -u -N /tmp/LIVE-1/<object> /tmp/MERGED-1/<object>`.
func changedObjects(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		f := strings.Fields(l)
		if len(f) >= 4 && f[0] == "diff" && strings.Contains(f[len(f)-2], "/LIVE-") {
			out = append(out, path.Base(f[len(f)-2]))
		}
	}
	return out
}

// apply runs kubectl apply on the Control Plane.
func apply(ctx context.Context, h *stage.Host, name string, manifest []byte, t Target) error {
	p, err := put(ctx, h, name, manifest)
	if err != nil {
		return err
	}
	_, err = h.Exec.Run(ctx, adminEnv+"kubectl apply "+t.flags()+" -f "+p)
	return err
}

// head returns the first n lines of s.
func head(s string, n int) string {
	lines := strings.SplitN(strings.TrimSpace(s), "\n", n+1)
	if len(lines) > n {
		lines = append(lines[:n], "…")
	}
	return strings.Join(lines, "\n")
}
