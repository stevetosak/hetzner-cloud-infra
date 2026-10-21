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
	// An interrupt, SIGTERM or SIGHUP (the terminal closed) cancels the run's
	// context, so deferred clean-ups such as the bootstrap SSH close still run
	// (ADR 0009). A SIGKILL or a crash cannot be caught: the next run warns
	// about an open port (sshgate.WarnOpen).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	if err := cmd.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
