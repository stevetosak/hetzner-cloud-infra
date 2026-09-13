// Package cmd wires up the kluster CLI: cobra command tree, global flags,
// and config loading shared by every subcommand.
package cmd

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/config"
)

var (
	configPath string
	verbose    bool
	sshKeyPath string

	cfg    *config.Config
	logger *slog.Logger
)

// rootCmd is the `kluster` entrypoint. Each subcommand file's init()
// registers itself onto this.
var rootCmd = &cobra.Command{
	Use:   "kluster",
	Short: "Bootstrap and manage the Hetzner Kubernetes cluster's nodes",
	Long: "kluster wraps Terraform and SSH to bootstrap the cluster's control plane and workers, " +
		"and to add or remove a single worker node without a full cluster reset.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		level := slog.LevelInfo
		if verbose {
			level = slog.LevelDebug
		}
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

		// `kluster version` and `kluster --help` shouldn't require a config file.
		if cmd.Name() == "version" || cmd.Name() == "help" {
			return nil
		}

		loaded, err := config.Load(configPath)
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}
		cfg = loaded
		return nil
	},
}

// Execute runs the CLI. Called from main.go.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "kluster.yaml", "path to kluster.yaml")
	rootCmd.PersistentFlags().BoolVar(&verbose, "verbose", false, "enable debug logging")
	rootCmd.PersistentFlags().StringVar(&sshKeyPath, "ssh-key", "", "path to an SSH private key (default: use ssh-agent, via SSH_AUTH_SOCK)")
}
