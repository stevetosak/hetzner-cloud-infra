package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/coreset"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/filediff"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/tf"
)

var projectAddFlags struct {
	component, repo, tagPrefix, host string
}

func init() {
	f := projectAddCmd.Flags()
	f.StringVar(&projectAddFlags.component, "component", "", "also add the deploy catalog row of this component (Application <project>-<component>)")
	f.StringVar(&projectAddFlags.repo, "repo", "", "with --component: its app repository, owner/name; without it the row says null and resolves no version")
	f.StringVar(&projectAddFlags.tagPrefix, "tag-prefix", "v", "with --component: its release-tag prefix")
	f.StringVar(&projectAddFlags.host, "host", "", "with --component: its public host, for the GitHub Deployment link (default <app>.tosak.net)")
	projectCmd.AddCommand(projectAddCmd)
	rootCmd.AddCommand(projectCmd)
}

var projectCmd = &cobra.Command{
	Use:   "project",
	Short: "The projects ArgoCD deploys from projects/ (ADR 0003)",
}

var projectAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Register a project everywhere services check asks for it; writes the working tree only",
	Long: "Adds what a project needs and lacks: its line in the projects generator, its AppProject, " +
		"its namespace in the core AppProject and on both Gateway listeners, its LimitRange directory " +
		"and listing, projects/<name>/namespace.yaml and, with --component, the deploy catalog row. " +
		"A registered project gives no change, so a run also repairs a project with a part missing. " +
		"The edits keep every comment, and a result that adds a refusal to services check is refused. " +
		"The component's own files (Deployment, overlay, Service, route, Probe target) stay hand-written. " +
		"Plan Mode prints the diff; --apply writes the files. Nothing touches the cluster.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if projectAddFlags.component == "" {
			for _, name := range []string{"repo", "tag-prefix", "host"} {
				if cmd.Flags().Changed(name) {
					return fmt.Errorf("--%s describes a component: give --component too", name)
				}
			}
		}
		root, err := repositoryRoot(cmd)
		if err != nil {
			return err
		}
		req := coreset.AddRequest{
			Project:   args[0],
			Component: projectAddFlags.component,
			Repo:      projectAddFlags.repo,
			TagPrefix: projectAddFlags.tagPrefix,
			Host:      projectAddFlags.host,
		}
		return projectAdd(root, req, flags.apply, cmd.OutOrStdout())
	},
}

// repositoryRoot returns the top of the repository: the directory of the
// Secret Catalog, which must be the git top level.
func repositoryRoot(cmd *cobra.Command) (string, error) {
	cfg, err := config.Load(flags.config)
	if err != nil {
		return "", err
	}
	catPath, err := cfg.Path(cfg.Secrets.Catalog)
	if err != nil {
		return "", err
	}
	root := filepath.Dir(catPath)
	if err := (gitRepo{root: root}).isTop(cmd.Context()); err != nil {
		return "", err
	}
	return root, nil
}

// projectAdd registers req in the repository at root: Plan Mode prints the
// edits, apply writes them and proves the written tree with the check.
func projectAdd(root string, req coreset.AddRequest, apply bool, out io.Writer) error {
	fmt.Fprintf(out, "kluster: project add %s, %s\n", req.Project, modeName(tf.Mode{Apply: apply}))
	reg, err := coreset.AddProject(os.DirFS(root), req)
	if err != nil {
		return err
	}
	for _, e := range reg.Standing {
		fmt.Fprintln(out, "note: services check refuses this already, not for the registration:", e)
	}
	if len(reg.Edits) == 0 {
		fmt.Fprintf(out, "%s is registered: nothing to change\n", req.Project)
		return nil
	}
	for _, e := range reg.Edits {
		if e.Old == nil {
			fmt.Fprintf(out, "\nnew file %s:\n", e.Path)
		}
		fmt.Fprint(out, filediff.Unified(e.Path, string(e.Old), string(e.New)))
	}
	if !apply {
		fmt.Fprintf(out, "\n%d file(s) would change; --apply writes them\n", len(reg.Edits))
		return nil
	}
	for _, e := range reg.Edits {
		path := filepath.Join(root, filepath.FromSlash(e.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, e.New, 0o644); err != nil {
			return err
		}
	}
	if errs := coreset.Check(os.DirFS(root)); len(errs) > len(reg.Standing) {
		return fmt.Errorf("the written files fail services check: %v", errs)
	}
	fmt.Fprintf(out, "\n%d file(s) written. By hand next (the manifests and add-project rules): each component's "+
		"base, overlay and Service; projects/%s/httproute.yaml with a Probe target per host; the projects tables in the docs. "+
		"After the merge: services seed --env live\n", len(reg.Edits), req.Project)
	return nil
}
