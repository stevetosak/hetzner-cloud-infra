package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/cluster"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/terraform"
)

var (
	nodeRemoveForce  bool
	nodeRemoveDryRun bool
)

var nodeRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Drain, delete, and deprovision a single worker node",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runNodeRemove(cmd.Context(), args[0])
	},
}

func init() {
	nodeRemoveCmd.Flags().BoolVar(&nodeRemoveForce, "force", false, "skip the drain grace period")
	nodeRemoveCmd.Flags().BoolVar(&nodeRemoveDryRun, "dry-run", false, "print commands without executing them")
	nodeCmd.AddCommand(nodeRemoveCmd)
}

func runNodeRemove(ctx context.Context, name string) error {
	path := tfvarsPath()

	workers, err := terraform.ReadWorkers(path)
	if err != nil {
		return err
	}
	if _, exists := workers[name]; !exists {
		return fmt.Errorf("worker %q not found in %s", name, path)
	}

	tf, err := terraformClient()
	if err != nil {
		return err
	}

	liveNames, err := resolveLiveNames(ctx, workers)
	if err != nil {
		return fmt.Errorf("resolving live server name for %q: %w", name, err)
	}
	liveName, ok := liveNames[name]
	if !ok {
		return fmt.Errorf("could not find a live server matching tfvars key %q", name)
	}
	logger.Info("removing worker", "name", name, "liveName", liveName)

	kubectl := cluster.Kubectl{}

	if nodeRemoveDryRun {
		logger.Info("[DRY-RUN] would drain node", "node", liveName, "force", nodeRemoveForce)
	} else if err := kubectl.DrainNode(ctx, liveName, nodeRemoveForce); err != nil {
		if !nodeRemoveForce {
			return fmt.Errorf("draining node %s (use --force to proceed and destroy it anyway): %w", liveName, err)
		}
		logger.Warn("drain failed, proceeding anyway because --force was set", "node", liveName, "error", err)
	}

	if nodeRemoveDryRun {
		logger.Info("[DRY-RUN] would delete node from cluster", "node", liveName)
	} else if err := kubectl.DeleteNode(ctx, liveName); err != nil {
		return fmt.Errorf("deleting node %s: %w", liveName, err)
	}

	if err := removeControlPlanePeer(ctx, name); err != nil {
		return err
	}

	if nodeRemoveDryRun {
		logger.Info("[DRY-RUN] would remove worker from terraform.tfvars and run terraform apply", "name", name)
	} else {
		if err := terraform.RemoveWorker(path, name); err != nil {
			return err
		}
		logger.Info("applying terraform to deprovision the server")
		if err := tf.Apply(ctx, terraformApplyVars(nodeSuffixNow())); err != nil {
			return err
		}
	}

	logger.Info("node removed", "name", name, "liveName", liveName)
	return nil
}

// removeControlPlanePeer strips the [Peer] block whose comment is name
// (the tfvars key, the same stable identifier both `bootstrap` and
// `node add` use) from the control plane's wg0.conf, then hot-reloads via
// `wg syncconf` — mirrors node add's incremental update, so removing one
// worker doesn't disrupt WireGuard sessions to the others.
func removeControlPlanePeer(ctx context.Context, name string) error {
	if nodeRemoveDryRun {
		logger.Info("[DRY-RUN] would remove peer block from control plane wg0.conf and run wg syncconf", "name", name)
		return nil
	}

	cp, err := dialSSH(cfg.ControlPlane.VpnIP, cfg.ControlPlane.SSHUser)
	if err != nil {
		return fmt.Errorf("dialing control plane at %s: %w", cfg.ControlPlane.VpnIP, err)
	}
	defer cp.Close()

	existing, err := cp.ReadFile(ctx, "/etc/wireguard/wg0.conf")
	if err != nil {
		return fmt.Errorf("reading control plane wg0.conf: %w", err)
	}

	allowedIPs, found := cluster.PeerAllowedIPsByComment(existing, name)
	if !found {
		logger.Warn("no WireGuard peer found for node, skipping WireGuard cleanup", "name", name)
		return nil
	}

	updated := cluster.RemovePeerBlock(existing, allowedIPs)
	if err := installWgConf(ctx, cp, updated, `bash -c "wg syncconf wg0 <(wg-quick strip wg0)"`); err != nil {
		return err
	}

	logger.Info("removed peer from control plane WireGuard config", "name", name)
	return nil
}
