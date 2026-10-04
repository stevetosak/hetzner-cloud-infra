// Command kluster builds the Hetzner Kubernetes cluster from its runbooks.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/cmd"
)

func main() {
	// An interrupt cancels the run's context, so deferred clean-ups such as
	// the bootstrap SSH close still run (ADR 0009).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cmd.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
