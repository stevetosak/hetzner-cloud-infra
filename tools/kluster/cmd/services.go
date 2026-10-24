package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/coreset"
)

func init() {
	servicesCmd.AddCommand(servicesCheckCmd)
	rootCmd.AddCommand(servicesCmd)
}

var servicesCmd = &cobra.Command{
	Use:   "services",
	Short: "What ArgoCD syncs from core/ and projects/ (ADR 0010)",
}

var servicesCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Prove the synced files agree with each other; reads files only (CI runs it)",
	Long: "Refuses a core kustomization that lists what only the seed or kluster applies " +
		"(CRDs, Namespaces, *.enc.yaml, pg-cluster.yaml, values), a rehearsal element the main " +
		"core list does not have, and a project in the projects generator that lacks its " +
		"AppProject, namespace file, Gateway listeners, deploy catalog entry or Reloader scope.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(flags.config)
		if err != nil {
			return err
		}
		// The catalog sits at the top of the repository.
		catPath, err := cfg.Path(cfg.Secrets.Catalog)
		if err != nil {
			return err
		}
		root := filepath.Dir(catPath)
		if err := (gitRepo{root: root}).isTop(cmd.Context()); err != nil {
			return err
		}
		fsys := os.DirFS(root)
		if errs := coreset.Check(fsys); len(errs) > 0 {
			for _, e := range errs {
				fmt.Fprintln(cmd.ErrOrStderr(), "refused:", e)
			}
			return fmt.Errorf("%d problem(s) in the synced files", len(errs))
		}
		core, err := coreset.Core(fsys)
		if err != nil {
			return err
		}
		projects, err := coreset.Projects(fsys)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%d core Application(s) and %d project(s) checked: the synced files agree\n",
			len(core), len(projects))
		return nil
	},
}
