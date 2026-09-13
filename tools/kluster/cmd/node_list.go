package cmd

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/cluster"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/terraform"
)

var nodeListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show current workers from terraform state and kubectl",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runNodeList(cmd.Context())
	},
}

func init() {
	nodeCmd.AddCommand(nodeListCmd)
}

func runNodeList(ctx context.Context) error {
	workers, err := terraform.ReadWorkers(tfvarsPath())
	if err != nil {
		return err
	}

	names, err := resolveLiveNames(ctx, workers)
	if err != nil {
		logger.Warn("could not resolve live worker names", "error", err)
		names = map[string]string{}
	}

	readiness, err := cluster.Kubectl{}.GetNodes(ctx)
	if err != nil {
		logger.Warn("could not read kubectl node status", "error", err)
		readiness = map[string]bool{}
	}

	keys := make([]string, 0, len(workers))
	for k := range workers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Printf("%-10s %-14s %-12s %-24s %s\n", "NAME", "PRIVATE_IP", "TYPE", "LIVE_NAME", "READY")
	for _, k := range keys {
		w := workers[k]
		liveName := names[k]
		ready := "unknown"
		if r, ok := readiness[liveName]; ok {
			if r {
				ready = "true"
			} else {
				ready = "false"
			}
		}
		fmt.Printf("%-10s %-14s %-12s %-24s %s\n", k, w.PrivateIP, w.ServerType, liveName, ready)
	}
	return nil
}

// liveNamesByTfvarsKey maps each tfvars key to its live server name
// ("<key>-<node_suffix>") by prefix match against terraform's worker_names
// output.
func liveNamesByTfvarsKey(workers map[string]terraform.Worker, liveNames []string) map[string]string {
	result := make(map[string]string, len(workers))
	for key := range workers {
		for _, live := range liveNames {
			if len(live) > len(key) && live[:len(key)+1] == key+"-" {
				result[key] = live
				break
			}
		}
	}
	return result
}

func resolveLiveNames(ctx context.Context, workers map[string]terraform.Worker) (map[string]string, error) {
	tf, err := terraformClient()
	if err != nil {
		return nil, err
	}
	liveNames, err := tf.WorkerNames(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading live worker names (is terraform applied?): %w", err)
	}
	return liveNamesByTfvarsKey(workers, liveNames), nil
}
