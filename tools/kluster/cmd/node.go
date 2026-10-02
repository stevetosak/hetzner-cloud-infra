package cmd

import "github.com/spf13/cobra"

var nodeCmd = &cobra.Command{
	Use:   "node",
	Short: "Manage individual worker nodes",
}

func init() {
	rootCmd.AddCommand(nodeCmd)
}
