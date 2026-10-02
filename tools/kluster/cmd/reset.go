package cmd

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/cluster"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/pipeline"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/terraform"
)

var resetDryRun bool

var resetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Delete and recreate every worker node, then re-bootstrap the cluster",
	Long: "Deletes every worker from Kubernetes, taints its Terraform resource for " +
		"recreation, applies, and re-runs the full bootstrap pipeline against the " +
		"recreated workers. Ports infra/scripts/bootstrap/reset-nodes.sh, folding in " +
		"the re-bootstrap step operators previously ran by hand.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runReset(cmd.Context())
	},
}

func init() {
	resetCmd.Flags().BoolVar(&resetDryRun, "dry-run", false, "print commands without executing them")
	rootCmd.AddCommand(resetCmd)
}

func runReset(ctx context.Context) error {
	path := tfvarsPath()
	workers, err := terraform.ReadWorkers(path)
	if err != nil {
		return err
	}
	if len(workers) == 0 {
		return fmt.Errorf("no workers found in %s", path)
	}

	names, err := resolveLiveNames(ctx, workers)
	if err != nil {
		return fmt.Errorf("resolving live server names: %w", err)
	}

	keys := make([]string, 0, len(workers))
	for k := range workers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	kubectl := cluster.Kubectl{}
	for _, key := range keys {
		liveName, ok := names[key]
		if !ok {
			logger.Warn("no live server found for tfvars key, skipping k8s delete", "key", key)
			continue
		}
		if resetDryRun {
			logger.Info("[DRY-RUN] would delete node from cluster", "node", liveName)
		} else if err := kubectl.DeleteNode(ctx, liveName); err != nil {
			logger.Warn("deleting node failed, continuing reset", "node", liveName, "error", err)
		}
	}

	tf, err := terraformClient()
	if err != nil {
		return err
	}

	for _, key := range keys {
		address := fmt.Sprintf(`hcloud_server.workers["%s"]`, key)
		if resetDryRun {
			logger.Info("[DRY-RUN] would taint", "address", address)
		} else if err := tf.Taint(ctx, address); err != nil {
			return err
		}
	}

	nodeSuffix := nodeSuffixNow()
	if resetDryRun {
		logger.Info("[DRY-RUN] would run terraform apply", "vars", terraformApplyVars(nodeSuffix))
	} else {
		logger.Info("applying terraform to recreate tainted workers")
		if err := tf.Apply(ctx, terraformApplyVars(nodeSuffix)); err != nil {
			return err
		}
	}

	logger.Info("re-bootstrapping recreated workers")
	return runBootstrap(ctx, &pipeline.RunOptions{DryRun: resetDryRun, Logger: logger})
}
