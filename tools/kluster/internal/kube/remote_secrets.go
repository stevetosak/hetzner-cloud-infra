package kube

import (
	"context"
	"fmt"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/local"
)

// SecretDir is where a Secret manifest waits on the Control Plane for the
// one kubectl apply that reads it: /run is a tmpfs, so a value never lands
// on a disk, and the file is removed whatever the apply does.
const SecretDir = "/run/kluster"

// Secret returns the live Secret as YAML, or nil when it does not exist.
// With Apply, Kubectl is a secrets.Cluster that works in a rehearsal too,
// unlike Local (see the package comment). The output holds values: it is for
// comparing, never for printing.
func (k Kubectl) Secret(ctx context.Context, ref catalog.Ref) ([]byte, error) {
	out, err := k.Exec.Run(ctx, "KUBECONFIG="+AdminConf+" kubectl get secret -n "+local.Quote(ref.Namespace)+" "+
		local.Quote(ref.Name)+" -o yaml --ignore-not-found 2>/dev/null")
	if err != nil {
		// The message would hold the output, which is the Secret.
		return nil, fmt.Errorf("kubectl get secret %s failed", ref)
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	return []byte(out), nil
}

// Apply applies one Secret manifest server-side as FieldManager, from a file
// in SecretDir. See Local.Apply for force.
func (k Kubectl) Apply(ctx context.Context, manifest []byte, force bool) error {
	if _, err := k.Exec.Run(ctx, "mkdir -p -m 700 "+SecretDir); err != nil {
		return err
	}
	file := SecretDir + "/secret.yaml"
	if err := k.Exec.WriteFile(ctx, file, string(manifest), 0o600); err != nil {
		return err
	}
	flags := "--server-side --field-manager=" + FieldManager
	if force {
		flags += " --force-conflicts"
	}
	out, err := k.Exec.Run(ctx, "KUBECONFIG="+AdminConf+" kubectl apply "+flags+" -f "+file+" 2>&1; rc=$?; rm -f "+file+"; exit $rc")
	if err != nil {
		return fmt.Errorf("kubectl apply: %s", scrub([]byte(out), manifest))
	}
	return nil
}
