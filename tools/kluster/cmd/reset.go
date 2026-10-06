package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/tf"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

func init() {
	rootCmd.AddCommand(resetCmd)
}

var resetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Replace every Worker, one at a time (docs/runbook/workers.md)",
	Long: "A Reset is node replace of each Worker the environment's Worker set declares, in name order. " +
		"The next Worker starts only once the one before is a Ready Node and every CNPG Cluster has all " +
		"its instances ready again (kluster waits up to 15 minutes for that); the first one is refused " +
		"at once while a CNPG instance is not ready. Every declared Worker must have a server. The " +
		"Control Plane is not touched. A Reset that stops says which Workers it replaced and which are left.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, reset)
	},
}

// resetDatabaseWait is how long a Reset waits for the CNPG instances to be
// ready again between two Workers.
const resetDatabaseWait = 15 * time.Minute

func reset(ctx context.Context, a *app) error {
	path, err := a.workerSetPath()
	if err != nil {
		return err
	}
	set, err := workerset.Load(path)
	if err != nil {
		return err
	}
	ws := set.Workers()
	if len(ws) == 0 {
		fmt.Fprintf(a.out, "%s declares no Worker: nothing to reset\n", path)
		return nil
	}
	servers := make([]*cloud.Server, len(ws))
	var missing []string
	for i, w := range ws {
		if servers[i], err = a.api.Server(ctx, w.Name); err != nil {
			return err
		}
		if servers[i] == nil {
			missing = append(missing, w.Name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the project has no server for %s: node add builds each first, then reset", strings.Join(missing, ", "))
	}
	fmt.Fprintf(a.out, "reset: replace %s, one at a time\n", strings.Join(workerNames(ws), ", "))

	return a.withReplaceAccess(ctx, func(workers *tf.Module, cpSrv *cloud.Server, cp *stage.Host) error {
		for i, w := range ws {
			if i > 0 && cp != nil {
				if err := a.waitDatabases(ctx, cp, 10*time.Second, resetDatabaseWait); err != nil {
					return resetStopped(ws, i, err)
				}
			}
			if err := a.replaceWorker(ctx, workers, w, servers[i], cpSrv, cp); err != nil {
				return resetStopped(ws, i, err)
			}
		}
		if a.mode.Apply {
			fmt.Fprintf(a.out, "reset: every Worker replaced: %s\n", strings.Join(workerNames(ws), ", "))
		}
		return nil
	})
}

// resetStopped says where a Reset stopped: the Workers before ws[i] are
// replaced, ws[i] failed, the rest were not touched.
func resetStopped(ws []workerset.Worker, i int, err error) error {
	done := "none"
	if i > 0 {
		done = strings.Join(workerNames(ws[:i]), ", ")
	}
	left := "none"
	if i+1 < len(ws) {
		left = strings.Join(workerNames(ws[i+1:]), ", ")
	}
	return fmt.Errorf("reset stopped at %s (replaced: %s; not touched: %s): %w", ws[i].Name, done, left, err)
}

func workerNames(ws []workerset.Worker) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Name
	}
	return out
}
