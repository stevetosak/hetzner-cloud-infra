package stages

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/local"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// stubbedBash runs a probe in real bash with a stub kubectl first on PATH,
// so the shell around each check is tested, not only its text.
type stubbedBash struct {
	local.Exec
	dir string
}

func (s stubbedBash) Run(ctx context.Context, cmd string) (string, error) {
	return s.Exec.Run(ctx, "export PATH="+local.Quote(s.dir)+":$PATH\n"+cmd)
}

// newStub makes a kubectl that prints out for any jsonpath query naming
// field, and succeeds silently for everything else.
func newStub(t *testing.T, script string) stubbedBash {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte("#!/usr/bin/env bash\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return stubbedBash{dir: dir}
}

// The 2026-10-04 rehearsal: `! export KUBECONFIG=…` negated the export, so
// the check passed with the taint present and failed once it was gone.
func TestCloudControllerProbeInBash(t *testing.T) {
	c := CloudController{Network: "tosak-net", Node: "k8s-cp", ServerID: "42", PrivateIP: "10.0.1.5"}
	common := `case "$*" in
  *'{.data.network}'*) printf %s dG9zYWstbmV0 ;;
  *'{.data.token}'*) printf %s c2VjcmV0 ;;
  *'{.spec.providerID}'*) printf %s hcloud://42 ;;
  *InternalIP*) printf %s 10.0.1.5 ;;
  *'{.spec.taints[*].key}'*) printf %s "$TAINTS" ;;
esac
`
	ctx := context.Background()
	cleared := newStub(t, "TAINTS=node-role.kubernetes.io/control-plane\n"+common)
	st, err := c.Probe(ctx, &stage.Host{Exec: cleared})
	if err != nil || !st.Done {
		t.Fatalf("taint cleared, want done: %+v, %v", st, err)
	}
	tainted := newStub(t, "TAINTS='node-role.kubernetes.io/control-plane node.cloudprovider.kubernetes.io/uninitialized'\n"+common)
	st, err = c.Probe(ctx, &stage.Host{Exec: tainted})
	if err != nil || st.Done || st.Detail != "not yet: initialized" {
		t.Fatalf("taint present, want only initialized failing: %+v, %v", st, err)
	}
}
