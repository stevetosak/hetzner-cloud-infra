// Command kluster bootstraps and manages the Hetzner Kubernetes cluster's
// nodes: full-cluster bootstrap/reset, and single-node add/remove.
package main

import (
	"fmt"
	"os"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
