package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/bitwarden"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/secrets"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/sops"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/tf"
)

// ErrDrift is returned by `secrets diff` when a live Secret differs from its
// file, so the exit status is 1, as `scripts/secrets.sh diff` had it.
var ErrDrift = errors.New("live Secrets differ from their files")

var secretsFlags struct {
	all     bool
	staged  bool
	noVault bool
	attach  bool
}

func init() {
	secretsRotateCmd.Flags().BoolVar(&secretsFlags.all, "all", false, "rotate every generated key")
	secretsCheckCmd.Flags().BoolVar(&secretsFlags.staged, "staged", false, "check what is staged for the next commit, not the working tree")
	secretsListCmd.Flags().BoolVar(&secretsFlags.noVault, "no-bitwarden", false, "do not compare issued keys with Bitwarden")
	secretsListCmd.Flags().BoolVar(&secretsFlags.attach, "attachments", false, "also compare attachments with Bitwarden (reads them with bw, a few seconds)")
	secretsCmd.AddCommand(secretsListCmd, secretsGenerateCmd, secretsRotateCmd, secretsPullCmd, secretsSetCmd,
		secretsDiffCmd, secretsApplyCmd, secretsEditCmd, secretsCheckCmd)
	rootCmd.AddCommand(secretsCmd)
}

var secretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "The Secret Catalog: make, rotate, check and apply the encrypted Secrets",
	Long: "Every *.enc.yaml file is listed in " + catalog.FileName + " with where each key's value comes " +
		"from (ADR 0003, Amendment 2026-10-06). The age key and the issued values are read from " +
		"Bitwarden. No command prints a value. Writes run in Plan Mode unless given --apply.",
}

// secretsApp is what one secrets command needs. Bitwarden and the age key
// are reached only when a command reads a value.
type secretsApp struct {
	cfg   *config.Config
	cat   *catalog.Catalog
	sops  *sops.Binary
	bw    *secrets.Bitwarden
	store *secrets.Store
	out   io.Writer
	apply bool
}

// secretsRun loads the catalog and the pinned sops, then calls fn.
func secretsRun(cmd *cobra.Command, fn func(ctx context.Context, a *secretsApp) error) error {
	ctx := cmd.Context()
	cfg, err := config.Load(flags.config)
	if err != nil {
		return err
	}
	catPath, err := cfg.Path(cfg.Secrets.Catalog)
	if err != nil {
		return err
	}
	cat, err := catalog.Load(catPath)
	if err != nil {
		return err
	}
	sopsConfig, err := cfg.Path(cfg.Secrets.SopsConfig)
	if err != nil {
		return err
	}
	bin := sops.New(cfg.Versions.Sops, sopsConfig)
	if err := bin.CheckVersion(ctx); err != nil {
		return err
	}
	bw := secrets.NewBitwarden(bitwarden.New(cmd.ErrOrStderr()))
	a := &secretsApp{
		cfg:   cfg,
		cat:   cat,
		sops:  bin,
		bw:    bw,
		store: secrets.NewStore(cat, bin, bw.AgeKey),
		out:   cmd.OutOrStdout(),
		apply: flags.apply,
	}
	fmt.Fprintf(a.out, "kluster: secrets, %s\n", modeName(tf.Mode{Apply: a.apply}))
	return fn(ctx, a)
}

// write prints a plan and, with --apply, writes it.
func (a *secretsApp) write(ctx context.Context, p *secrets.Planner, plan *secrets.Plan, err error) error {
	if err != nil {
		return err
	}
	if err := plan.Print(a.out); err != nil {
		return err
	}
	if !a.apply || !plan.Changed() {
		return nil
	}
	if err := p.Write(ctx, plan); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "written; review with `git diff --stat` and commit the files by name")
	return nil
}

var secretsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List every key with its kind and state; compares copies with their source and issued keys with Bitwarden",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return secretsRun(cmd, func(ctx context.Context, a *secretsApp) error {
			a.bw.SkipAttachments = !secretsFlags.attach
			var vault secrets.Vault = a.bw
			if secretsFlags.noVault {
				vault = nil
			}
			rows, err := secrets.List(ctx, a.store, vault)
			if err != nil {
				return err
			}
			return secrets.PrintList(a.out, rows)
		})
	},
}

var secretsGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Make every missing generated value and rewrite every copy and literal",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return secretsRun(cmd, func(ctx context.Context, a *secretsApp) error {
			p := secrets.NewPlanner(a.store, nil)
			plan, err := p.Generate(ctx)
			return a.write(ctx, p, plan, err)
		})
	},
}

var secretsRotateCmd = &cobra.Command{
	Use:   "rotate (<namespace>/<name>.<key>... | --all)",
	Short: "Give generated keys a new value and rewrite every copy of them in the same write",
	Long: "An issued key is rotated at its provider: issue the new value there, store it in " +
		"Bitwarden, then run `kluster secrets pull`. After a rotate, apply the files and let " +
		"the readers restart.",
	RunE: func(cmd *cobra.Command, args []string) error {
		if secretsFlags.all == (len(args) > 0) {
			return errors.New("name the keys to rotate, or give --all")
		}
		refs, err := keyRefs(args)
		if err != nil {
			return err
		}
		return secretsRun(cmd, func(ctx context.Context, a *secretsApp) error {
			p := secrets.NewPlanner(a.store, nil)
			plan, err := p.Rotate(ctx, refs)
			return a.write(ctx, p, plan, err)
		})
	},
}

var secretsPullCmd = &cobra.Command{
	Use:   "pull [<namespace>/<name>.<key>...]",
	Short: "Read issued values from Bitwarden, every issued key or the named ones",
	RunE: func(cmd *cobra.Command, args []string) error {
		refs, err := keyRefs(args)
		if err != nil {
			return err
		}
		return secretsRun(cmd, func(ctx context.Context, a *secretsApp) error {
			p := secrets.NewPlanner(a.store, a.bw)
			plan, err := p.Pull(ctx, refs)
			return a.write(ctx, p, plan, err)
		})
	},
}

var secretsSetCmd = &cobra.Command{
	Use:   "set <namespace>/<name>.<key>",
	Short: "Type one issued value on the terminal, with no echo (store the same value in Bitwarden yourself: kluster never writes the vault)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		refs, err := keyRefs(args)
		if err != nil {
			return err
		}
		return secretsRun(cmd, func(ctx context.Context, a *secretsApp) error {
			s, _ := a.cat.Secret(refs[0].Secret)
			if s != nil {
				if k := s.Keys[refs[0].Key]; k != nil && k.Attachment != "" {
					return fmt.Errorf("%s is a Bitwarden attachment: a file cannot be typed; attach it to %s and run `kluster secrets pull`", refs[0], k.Item)
				}
			}
			v, err := readSecret(cmd.ErrOrStderr(), refs[0].String())
			if err != nil {
				return err
			}
			p := secrets.NewPlanner(a.store, nil)
			plan, err := p.Set(ctx, refs[0], v)
			return a.write(ctx, p, plan, err)
		})
	},
}

var secretsDiffCmd = &cobra.Command{
	Use:   "diff [<namespace>/<name>...]",
	Short: "Compare the files with the live Secrets by key name; exit status 1 when they differ",
	RunE: func(cmd *cobra.Command, args []string) error {
		return secretsRun(cmd, func(ctx context.Context, a *secretsApp) error {
			_, drift, err := a.liveDiff(ctx, args)
			if err == nil && drift {
				err = ErrDrift
			}
			return err
		})
	},
}

var secretsApplyCmd = &cobra.Command{
	Use:   "apply [<namespace>/<name>...]",
	Short: "Apply the Secrets that differ from their files to the live cluster, server-side",
	Long: "Plan Mode prints the diff. --apply applies each drifting Secret server-side as field " +
		"manager \"" + kube.FieldManager + "\" and never forces a conflict. Live only. A Secret with a " +
		"missing key is refused.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return secretsRun(cmd, func(ctx context.Context, a *secretsApp) error {
			diffs, drift, err := a.liveDiff(ctx, args)
			if err != nil || !drift || !a.apply {
				return err
			}
			cl, err := a.cluster()
			if err != nil {
				return err
			}
			if err := secrets.Apply(ctx, a.store, cl, diffs); err != nil {
				return err
			}
			fmt.Fprintln(a.out, "applied; reading back:")
			_, drift, err = a.liveDiff(ctx, args)
			if err == nil && drift {
				err = errors.New("the live Secrets still differ after apply: a key another field manager owns is not removed")
			}
			return err
		})
	},
}

var secretsEditCmd = &cobra.Command{
	Use:   "edit <namespace>/<name>",
	Short: "Open one file in sops edit; for what list, generate, pull and set do not cover",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return secretsRun(cmd, func(ctx context.Context, a *secretsApp) error {
			sel, err := secrets.Select(a.cat, args)
			if err != nil {
				return err
			}
			s := sel[0]
			if !a.apply {
				fmt.Fprintf(a.out, "would open %s in sops edit\n", s.File)
				return nil
			}
			key, err := a.bw.AgeKey(ctx)
			if err != nil {
				return err
			}
			changed, err := a.sops.Edit(ctx, a.cat.Path(s), key)
			if err != nil || !changed {
				if err == nil {
					fmt.Fprintln(a.out, "no change")
				}
				return err
			}
			rows, err := secrets.List(ctx, a.store, nil)
			if err != nil {
				return fmt.Errorf("%s after the edit: %w", s.File, err)
			}
			var own []secrets.Row
			for _, r := range rows {
				if r.Secret == s.Ref {
					own = append(own, r)
				}
			}
			return secrets.PrintList(a.out, own)
		})
	},
}

var secretsCheckCmd = &cobra.Command{
	Use:   "check [--staged]",
	Short: "Prove every *.enc.yaml is listed and fully encrypted; needs no key (the pre-commit hook and CI run it)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		cfg, err := config.Load(flags.config)
		if err != nil {
			return err
		}
		catPath, err := cfg.Path(cfg.Secrets.Catalog)
		if err != nil {
			return err
		}
		root := filepath.Dir(catPath)
		g := gitRepo{root: root}
		if err := g.isTop(ctx); err != nil {
			return err
		}
		var cat *catalog.Catalog
		var files []secrets.File
		if secretsFlags.staged {
			data, err := g.run(ctx, "show", ":"+catalog.FileName)
			if err != nil {
				return fmt.Errorf("the staged %s: %w", catalog.FileName, err)
			}
			if cat, err = catalog.Parse(data, root); err != nil {
				return err
			}
			files, err = g.files(ctx, true)
			if err != nil {
				return err
			}
		} else {
			if cat, err = catalog.Load(catPath); err != nil {
				return err
			}
			if files, err = g.files(ctx, false); err != nil {
				return err
			}
		}
		out := cmd.OutOrStdout()
		if errs := secrets.Check(cat, files); len(errs) > 0 {
			for _, e := range errs {
				fmt.Fprintln(cmd.ErrOrStderr(), "refused:", e)
			}
			return fmt.Errorf("%d problem(s) in the encrypted files", len(errs))
		}
		if !secretsFlags.staged {
			for _, f := range secrets.Absent(cat, func(rel string) bool {
				_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
				return err == nil
			}) {
				fmt.Fprintf(out, "note: %s does not exist yet (`kluster secrets generate` or `pull` makes it)\n", f)
			}
		}
		fmt.Fprintf(out, "%d encrypted file(s) checked: listed in %s and fully encrypted\n", len(files), catalog.FileName)
		return nil
	},
}

// liveDiff refuses the rehearsal env, then compares the selected Secrets
// with the live cluster and prints the result.
func (a *secretsApp) liveDiff(ctx context.Context, names []string) ([]secrets.LiveDiff, bool, error) {
	cl, err := a.cluster()
	if err != nil {
		return nil, false, err
	}
	sel, err := secrets.Select(a.cat, names)
	if err != nil {
		return nil, false, err
	}
	diffs, err := secrets.Diff(ctx, a.store, cl, sel)
	if err != nil {
		return nil, false, err
	}
	drift, err := secrets.PrintDiff(a.out, diffs)
	return diffs, drift, err
}

// cluster is the live cluster, through the live laptop kubeconfig and its
// named context. A rehearsal has no Secrets of its own to apply.
func (a *secretsApp) cluster() (*kube.Local, error) {
	if flags.env != config.EnvLive {
		return nil, errors.New("kluster secrets diff and apply act on the live cluster only; drop --env " + flags.env)
	}
	laptop := a.cfg.Envs[config.EnvLive].Laptop
	kc, err := a.cfg.Path(laptop.Kubeconfig)
	if err != nil {
		return nil, err
	}
	return kube.NewLocal(kc, kube.ContextName(laptop.ClusterName)), nil
}

func keyRefs(args []string) ([]catalog.KeyRef, error) {
	refs := make([]catalog.KeyRef, 0, len(args))
	for _, s := range args {
		r, err := catalog.ParseKeyRef(s)
		if err != nil {
			return nil, err
		}
		refs = append(refs, r)
	}
	return refs, nil
}

// readSecret reads one value from the terminal twice, with no echo. A
// value never comes from a pipe: it would sit in a shell history or a file.
func readSecret(prompt io.Writer, name string) ([]byte, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, errors.New("set reads the value from a terminal only, with no echo; run it in your own terminal")
	}
	fmt.Fprintf(prompt, "value of %s (no echo): ", name)
	v, err := term.ReadPassword(fd)
	fmt.Fprintln(prompt)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(prompt, "again: ")
	again, err := term.ReadPassword(fd)
	fmt.Fprintln(prompt)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(v, again) {
		return nil, errors.New("the two values differ; nothing was written")
	}
	return bytes.TrimRight(v, "\r\n"), nil
}

// gitRepo reads the repository that holds the catalog.
type gitRepo struct{ root string }

func (g gitRepo) run(ctx context.Context, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, "git", append([]string{"-C", g.root}, args...)...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// isTop proves the catalog sits at the top of its repository, so git paths
// and catalog paths are the same.
func (g gitRepo) isTop(ctx context.Context) error {
	out, err := g.run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	top, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	if err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(g.root)
	if err != nil {
		return err
	}
	if top != root {
		return fmt.Errorf("%s must be at the top of the repository (%s)", catalog.FileName, top)
	}
	return nil
}

// files returns every *.enc.yaml with its content: the staged ones from the
// index, or the tracked and untracked ones from the working tree.
func (g gitRepo) files(ctx context.Context, staged bool) ([]secrets.File, error) {
	var list []byte
	var err error
	if staged {
		list, err = g.run(ctx, "diff", "--cached", "--name-only", "--diff-filter=ACMR", "-z", "--", "*"+catalog.EncSuffix)
	} else {
		list, err = g.run(ctx, "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", "*"+catalog.EncSuffix)
	}
	if err != nil {
		return nil, err
	}
	var files []secrets.File
	for _, p := range strings.Split(string(list), "\x00") {
		if p == "" {
			continue
		}
		var data []byte
		if staged {
			data, err = g.run(ctx, "show", ":"+p)
		} else {
			data, err = os.ReadFile(filepath.Join(g.root, filepath.FromSlash(p)))
			if errors.Is(err, os.ErrNotExist) {
				continue // deleted in the working tree, not yet staged
			}
		}
		if err != nil {
			return nil, err
		}
		files = append(files, secrets.File{Path: p, Data: data})
	}
	return files, nil
}
