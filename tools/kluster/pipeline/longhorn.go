package pipeline

import (
	"context"
	"fmt"
)

// LonghornStage installs Longhorn's node prerequisites: open-iscsi, the NFS
// kernel modules, and disabling multipathd (which otherwise claims the
// block devices Longhorn wants).
//
// Merges infra/scripts/bootstrap/pipeline/bootstrap_node-5-longhorn.sh
// (which only stopped multipathd) with infra/scripts/utils/longhorn_config.sh
// (which additionally installs open-iscsi, loads NFS modules, and disables+
// masks both multipathd and multipathd.socket) — the two were inconsistent
// duplicates before this rewrite.
type LonghornStage struct{}

func (LonghornStage) Name() string { return "longhorn" }

func (LonghornStage) Run(ctx context.Context, node *NodeContext, opts *RunOptions) error {
	if err := runCmd(ctx, node, opts, "apt-get install -y open-iscsi"); err != nil {
		return fmt.Errorf("installing open-iscsi: %w", err)
	}

	nfsModulesConf := "nfs\nnfsd\nlockd\nsunrpc\n"
	if err := writeFile(ctx, node, opts, "/etc/modules-load.d/nfs.conf", nfsModulesConf, 0o644); err != nil {
		return fmt.Errorf("writing NFS modules config: %w", err)
	}
	if err := runCmd(ctx, node, opts, "systemctl restart systemd-modules-load"); err != nil {
		return fmt.Errorf("reloading kernel modules: %w", err)
	}
	check(opts, node, "open-iscsi installed and NFS kernel modules loaded")

	multipathUnits := []string{"multipathd", "multipathd.socket"}
	for _, unit := range multipathUnits {
		for _, action := range []string{"stop", "disable", "mask"} {
			if err := runCmd(ctx, node, opts, fmt.Sprintf("systemctl %s %s", action, unit)); err != nil {
				return fmt.Errorf("%s %s: %w", action, unit, err)
			}
		}
	}
	check(opts, node, "multipathd disabled")

	return nil
}
