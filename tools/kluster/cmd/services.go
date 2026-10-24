package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/bitwarden"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/coreset"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/secrets"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/seed"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/sops"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

var seedFlags struct {
	timeout time.Duration
	diffDir string
}

func init() {
	servicesSeedCmd.Flags().DurationVar(&seedFlags.timeout, "timeout", 20*time.Minute, "how long core-healthy and lb-address wait")
	servicesSeedCmd.Flags().StringVar(&seedFlags.diffDir, "diff-dir", "", "Plan Mode: write each core Application's full diff to this directory")
	servicesCmd.AddCommand(servicesCheckCmd, servicesSeedCmd)
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

var servicesSeedCmd = &cobra.Command{
	Use:   "seed",
	Short: "Seed the CRDs, Namespaces and Secrets, install ArgoCD, wait for core and check the zone",
	Long: "Applies what ArgoCD cannot or must not own (ADR 0010, Amendment 2026-10-22): every " +
		"core/*/crds.yaml and namespace file and the Secrets, from the working tree; then the build " +
		"of core/argocd at the revision this environment's ArgoCD reads (live: the core " +
		"ApplicationSet's targetRevision; a rehearsal: branch rehearsal, put there with " +
		"`git push -f origin HEAD:rehearsal`). Then it waits until every core Application is " +
		"Synced and Healthy and the Gateway has its address, and refuses success while an A " +
		"record of a synced route points elsewhere; it writes no DNS record. A rehearsal makes " +
		"its Secrets fresh and leaves out each one marked rehearsal: omit. Plan Mode prints, " +
		"besides, what each core Application's first sync would change. kubectl runs on the " +
		"Control Plane.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, func(ctx context.Context, a *app) error { return a.seed(ctx, cmd) })
	},
}

func (a *app) seed(ctx context.Context, cmd *cobra.Command) error {
	rehearsal := !a.env.IsLive()
	catPath, err := a.cfg.Path(a.cfg.Secrets.Catalog)
	if err != nil {
		return err
	}
	root := filepath.Dir(catPath)
	repo := gitRepo{root: root}
	if err := repo.isTop(ctx); err != nil {
		return err
	}
	cat, err := catalog.Load(catPath)
	if err != nil {
		return err
	}
	bw := bitwarden.New(cmd.ErrOrStderr())

	rev, err := coreset.TargetRevision(os.DirFS(root), rehearsal)
	if err != nil {
		return err
	}
	synced, err := os.MkdirTemp("", "kluster-seed-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(synced)
	commit, err := seed.Export(ctx, repo.run, rev, synced)
	if err != nil {
		if rehearsal {
			return fmt.Errorf("%w; a rehearsal syncs branch %s: git push -f origin HEAD:%s", err, rev, rev)
		}
		return err
	}
	head, err := repo.run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "ArgoCD reads %s, at %s; CRDs, Namespaces and Secrets come from the working tree\n", rev, commit[:12])
	if h := strings.TrimSpace(string(head)); h != commit {
		fmt.Fprintf(a.out, "note: HEAD is %s, not %s: what ArgoCD owns is built from %s\n", h[:12], commit[:12], rev)
	}

	in := seed.Inputs{Rehearsal: rehearsal, WorkTree: root, Synced: synced, Catalog: cat,
		Timeout: seedFlags.timeout, Poll: 10 * time.Second}
	if !rehearsal {
		// Live applies each file's values; a rehearsal reads no file.
		sopsConfig, err := a.cfg.Path(a.cfg.Secrets.SopsConfig)
		if err != nil {
			return err
		}
		bin := sops.New(a.cfg.Versions.Sops, sopsConfig)
		if err := bin.CheckVersion(ctx); err != nil {
			return err
		}
		in.Store = secrets.NewStore(cat, bin, secrets.NewBitwarden(bw).AgeKey)
	}
	svc := a.cfg.Services
	item, err := bw.Item(ctx, svc.ZoneTokenItem)
	if err != nil {
		return err
	}
	token, err := item.Field(svc.ZoneTokenField)
	if err != nil {
		return err
	}
	in.Zone = &seed.Cloudflare{Zone: svc.Zone, Token: token}

	plan, err := seed.Build(in)
	if err != nil {
		return err
	}
	if seedFlags.diffDir != "" && a.mode.Apply {
		return fmt.Errorf("--diff-dir is for Plan Mode")
	}
	return a.onControlPlane(ctx, func(cp *stage.Host) error {
		if cp == nil {
			return stage.PlanNew(ctx, plan.Stages, a.cfg.ControlPlane.Name, a.out)
		}
		if err := stage.Run(ctx, plan.Stages, cp, stage.Options{Apply: a.mode.Apply, Out: a.out}); err != nil {
			return err
		}
		if a.mode.Apply {
			return nil
		}
		fmt.Fprintln(a.out, "What each core Application's sync would change (information only; ArgoCD applies it):")
		return plan.Diffs(ctx, cp, a.out, seedFlags.diffDir)
	})
}
