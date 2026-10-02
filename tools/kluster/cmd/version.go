package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// version is set via -ldflags "-X .../cmd.version=..." at build time.
var version = "dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the kluster version",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println(version)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
