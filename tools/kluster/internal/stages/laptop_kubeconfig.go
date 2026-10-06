package stages

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kubeconfig"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/local"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// LaptopKubeconfig gives the operator the new cluster's kubeconfig. Merged,
// every old entry under the same names is removed first, because a rebuilt
// cluster reuses the VPN address and a plain merge would keep the dead
// cluster's credentials under a name that looks right. The new context must
// answer before current-context moves to it. Not merged, the file holds the
// new cluster alone. Run on the laptop host (internal/local).
type LaptopKubeconfig struct {
	Path   string
	Merge  bool
	Names  kubeconfig.Names
	Conf   []byte // admin.conf renamed by kubeconfig.ForLaptop
	TmpDir string // the run directory
}

func (LaptopKubeconfig) Name() string { return "laptop-kubeconfig" }
func (LaptopKubeconfig) Runbook() string {
	return "docs/runbook/control-plane.md#6-kubeconfig-on-the-operator-workstation"
}

func (l LaptopKubeconfig) want() (kubeconfig.Cluster, error) {
	f, err := kubeconfig.Parse(l.Conf)
	if err != nil {
		return kubeconfig.Cluster{}, err
	}
	return f.Clusters[0].Cluster, nil
}

func (l LaptopKubeconfig) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	want, err := l.want()
	if err != nil {
		return stage.Status{}, err
	}
	data, err := h.Exec.ReadFile(ctx, l.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return stage.Status{Detail: l.Path + " does not exist"}, nil
	}
	if err != nil {
		return stage.Status{}, err
	}
	e, err := kubeconfig.Find([]byte(data), l.Names)
	if err != nil {
		return stage.Status{}, err
	}
	switch {
	case e.Cluster == nil || !e.HasUser || !e.HasContext:
		return stage.Status{Detail: "no " + l.Names.Context + " yet"}, nil
	case e.Cluster.CAData != want.CAData:
		return stage.Status{Detail: l.Names.Cluster + " holds another cluster's CA"}, nil
	case e.Cluster.Server != want.Server:
		return stage.Status{Detail: l.Names.Cluster + " points at " + e.Cluster.Server}, nil
	case l.Merge && e.CurrentContext != l.Names.Context:
		return stage.Status{Detail: "current-context is " + e.CurrentContext}, nil
	}
	return stage.Status{Done: true, Detail: l.Names.Context + " → " + want.Server}, nil
}

// Preview names what changes and never shows the file: it holds a
// cluster-admin key.
func (l LaptopKubeconfig) Preview(context.Context, *stage.Host) (string, error) {
	want, err := l.want()
	if err != nil {
		return "", err
	}
	if !l.Merge {
		return fmt.Sprintf("# write %s (0600): cluster %s at %s, context %s, nothing else\n", l.Path, l.Names.Cluster, want.Server, l.Names.Context), nil
	}
	return fmt.Sprintf(`# back up %[1]s
# delete context %[2]s, cluster %[3]s and user %[4]s if present
# merge the new cluster %[3]s at %[5]s
kubectl --kubeconfig %[1]s --context %[2]s get --raw=/readyz   # must answer ok
kubectl --kubeconfig %[1]s config use-context %[2]s
`, l.Path, l.Names.Context, l.Names.Cluster, l.Names.User, want.Server), nil
}

func (l LaptopKubeconfig) Act(ctx context.Context, h *stage.Host) error {
	if !l.Merge {
		return h.Exec.WriteFile(ctx, l.Path, string(l.Conf), 0o600)
	}
	kc := "kubectl --kubeconfig " + local.Quote(l.Path) + " "
	old, err := h.Exec.ReadFile(ctx, l.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		old = ""
	case err != nil:
		return err
	default:
		backup := l.Path + ".bak-kluster-" + time.Now().UTC().Format("20060102T150405Z")
		if err := h.Exec.WriteFile(ctx, backup, old, 0o600); err != nil {
			return err
		}
		e, err := kubeconfig.Find([]byte(old), l.Names)
		if err != nil {
			return err
		}
		if e.HasContext {
			if _, err := h.Exec.Run(ctx, kc+"config delete-context "+l.Names.Context); err != nil {
				return err
			}
		}
		if e.Cluster != nil {
			if _, err := h.Exec.Run(ctx, kc+"config delete-cluster "+l.Names.Cluster); err != nil {
				return err
			}
		}
		if e.HasUser {
			if _, err := h.Exec.Run(ctx, kc+"config delete-user "+l.Names.User); err != nil {
				return err
			}
		}
	}

	// The old file first, so its current-context stays until the new one
	// has answered.
	tmp := filepath.Join(l.TmpDir, "new-kubeconfig")
	if err := os.WriteFile(tmp, l.Conf, 0o600); err != nil {
		return err
	}
	defer os.Remove(tmp)
	merged, err := h.Exec.Run(ctx, "KUBECONFIG="+local.Quote(l.Path+":"+tmp)+" kubectl config view --flatten")
	if err != nil {
		return err
	}
	if err := h.Exec.WriteFile(ctx, l.Path, merged+"\n", 0o600); err != nil {
		return err
	}
	if out, err := h.Exec.Run(ctx, kc+"--context "+l.Names.Context+" get --raw=/readyz"); err != nil || out != "ok" {
		return fmt.Errorf("merged, but %s does not answer, so current-context was not moved (is the laptop's WireGuard up with the new hub key?): %v", l.Names.Context, err)
	}
	_, err = h.Exec.Run(ctx, kc+"config use-context "+l.Names.Context)
	return err
}
