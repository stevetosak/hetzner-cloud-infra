package cmd

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/bitwarden"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/db"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/pgcluster"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/r2"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/secrets"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/seed"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

var dbFlags struct {
	fresh, recover bool
	targetTime     string
	rehearsed      bool
	timeout        time.Duration
}

func init() {
	f := dbCreateCmd.Flags()
	f.BoolVar(&dbFlags.fresh, "fresh", false, "an empty database that archives to the generation the repository names")
	f.BoolVar(&dbFlags.recover, "recover", false, "restore the generation the repository names and archive to the next one")
	f.StringVar(&dbFlags.targetTime, "target-time", "", "--recover: stop replay at this time, RFC 3339 in UTC (2026-10-24T10:00:00Z)")
	f.BoolVar(&dbFlags.rehearsed, "rehearsed", false, "live --target-time: the time was looked at in a rehearsal first; every later write is lost")
	f.DurationVar(&dbFlags.timeout, "timeout", 30*time.Minute, "how long each waiting Stage waits")
	dbCreateCmd.MarkFlagsMutuallyExclusive("fresh", "recover")
	dbCreateCmd.MarkFlagsOneRequired("fresh", "recover")
	dbCmd.AddCommand(dbCreateCmd)
	rootCmd.AddCommand(dbCmd)
}

var dbCmd = &cobra.Command{
	Use:   "db",
	Short: "The database Cluster, which ArgoCD does not sync (ADR 0009)",
}

var dbCreateCmd = &cobra.Command{
	Use:   "create --fresh|--recover",
	Short: "Create the database Cluster into an empty slot and prove its backups",
	Long: "Applies the render of core/cnpg/cluster/fresh or core/cnpg/cluster/recover (ADR 0009, " +
		"Amendment 2026-10-24) and proves the Cluster healthy, archiving, free of the recovery " +
		"marker, replicating and backed up. It refuses while the Cluster exists, unless it is this " +
		"run's own render (a resume); it never changes or deletes a Cluster or a backup. A recovery " +
		"reads the live backups through the read-only source store and archives to the next " +
		"generation; after a live recovery kluster writes the generation bump into the working " +
		"tree, and the next db create refuses until it is committed. A rehearsal archives to its " +
		"own bucket and also proves a switchover (plugin#828). kubectl runs on the Control Plane. " +
		"Runbook: " + db.Runbook,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, func(ctx context.Context, a *app) error { return a.dbCreate(ctx, cmd) })
	},
}

// dbTargetTime reads --target-time and applies the live rule: a point in
// time on live is refused unless it was rehearsed (ADR 0009, Amendment
// 2026-10-24, decision 11).
func dbTargetTime(mode db.Mode, live bool) (*time.Time, error) {
	if dbFlags.targetTime == "" {
		if dbFlags.rehearsed {
			return nil, errors.New("--rehearsed goes with --target-time")
		}
		return nil, nil
	}
	if mode != db.Recover {
		return nil, errors.New("--target-time is for --recover")
	}
	if !strings.HasSuffix(dbFlags.targetTime, "Z") {
		return nil, fmt.Errorf("--target-time %q: give the time in UTC, ending in Z", dbFlags.targetTime)
	}
	t, err := time.Parse(time.RFC3339, dbFlags.targetTime)
	if err != nil {
		return nil, fmt.Errorf("--target-time: %w", err)
	}
	if live && !dbFlags.rehearsed {
		return nil, errors.New("a live --target-time loses every write after that time: find the time in a rehearsal first (--env rehearsal reads the live backups read only), then add --rehearsed")
	}
	return &t, nil
}

func (a *app) dbCreate(ctx context.Context, cmd *cobra.Command) error {
	live := a.env.IsLive()
	mode := db.Fresh
	if dbFlags.recover {
		mode = db.Recover
	}
	target, err := dbTargetTime(mode, live)
	if err != nil {
		return err
	}
	root, cat, err := a.repository(ctx)
	if err != nil {
		return err
	}
	// The render comes from the working tree, so what is applied must be
	// what is committed; an uncommitted bump stops the next run here.
	dirty, err := (gitRepo{root: root}).run(ctx, "status", "--porcelain", "--", pgcluster.ClusterDir)
	if err != nil {
		return err
	}
	if d := strings.TrimSpace(string(dirty)); d != "" {
		return fmt.Errorf("%s has changes that are not committed (a generation bump is committed before the next db create):\n%s", pgcluster.ClusterDir, d)
	}

	plan, err := a.dbPlan(ctx, bitwarden.New(cmd.ErrOrStderr()), root, cat, mode, target, dbFlags.timeout)
	if err != nil {
		return err
	}
	return a.onControlPlane(ctx, func(cp *stage.Host) error {
		if cp == nil {
			return stage.PlanNew(ctx, plan.Stages, a.cfg.ControlPlane.Name, a.out)
		}
		return stage.Run(ctx, plan.Stages, cp, stage.Options{Apply: a.mode.Apply, Out: a.out})
	})
}

// dbPlan opens the buckets with their tokens (and, for a live recovery,
// the lock rules) and builds the db create. It prints where the Cluster
// archives.
func (a *app) dbPlan(ctx context.Context, bw *bitwarden.Client, root string, cat *catalog.Catalog, mode db.Mode, target *time.Time, timeout time.Duration) (*db.Plan, error) {
	live := a.env.IsLive()
	archive, source, err := openStores(ctx, root, cat, secrets.NewBitwarden(bw), live)
	if err != nil {
		return nil, err
	}
	in := db.Inputs{Mode: mode, Live: live, Root: root, TargetTime: target, Archive: archive,
		Waiting: seed.Waiting{Timeout: timeout, Poll: 10 * time.Second}}
	if mode == db.Recover {
		in.Source = source
		if live {
			if in.Locks, err = a.lockReader(ctx, bw, root); err != nil {
				return nil, err
			}
		}
	}
	plan, err := db.Build(in)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(a.out, "db create --%s: the repository names %s; the Cluster archives to %s/%s\n",
		mode, plan.Generation.ServerName(), archive.Name(), plan.Archives.Prefix())
	return plan, nil
}

// repository is the repository top (where the catalog sits) and the
// catalog.
func (a *app) repository(ctx context.Context) (string, *catalog.Catalog, error) {
	catPath, err := a.cfg.Path(a.cfg.Secrets.Catalog)
	if err != nil {
		return "", nil, err
	}
	root := filepath.Dir(catPath)
	if err := (gitRepo{root: root}).isTop(ctx); err != nil {
		return "", nil, err
	}
	cat, err := catalog.Load(catPath)
	if err != nil {
		return "", nil, err
	}
	return root, cat, nil
}

// openStores opens the archive and the source bucket of this environment's
// ObjectStores, each with the token its Secret's catalog entry names: the
// live item, or the rehearsal item in a rehearsal. A rehearsal never reads a
// live token.
func openStores(ctx context.Context, root string, cat *catalog.Catalog, vault secrets.Vault, live bool) (archive, source r2.Bucket, err error) {
	as, ss, err := pgcluster.Stores(root, live)
	if err != nil {
		return nil, nil, err
	}
	if !live {
		// The guard behind kluster down's delete: a rehearsal's archive
		// bucket is never the live one.
		la, _, err := pgcluster.Stores(root, true)
		if err != nil {
			return nil, nil, err
		}
		if as.Bucket == la.Bucket {
			return nil, nil, fmt.Errorf("%s archives to the live bucket %s", pgcluster.RehearsalStoresDir, la.Bucket)
		}
	}
	open := func(s pgcluster.Store) (r2.Bucket, error) {
		cred, err := storeCredentials(ctx, cat, vault, s, live)
		if err != nil {
			return nil, err
		}
		return r2.Open(s.Endpoint, s.Bucket, cred)
	}
	if archive, err = open(as); err != nil {
		return nil, nil, err
	}
	if source, err = open(ss); err != nil {
		return nil, nil, err
	}
	return archive, source, nil
}

func storeCredentials(ctx context.Context, cat *catalog.Catalog, vault secrets.Vault, s pgcluster.Store, live bool) (r2.Credentials, error) {
	ref := catalog.Ref{Namespace: pgcluster.Namespace, Name: s.Secret}
	sec, ok := cat.Secret(ref)
	if !ok {
		return r2.Credentials{}, fmt.Errorf("ObjectStore %s reads Secret %s, which the catalog does not list", s.Name, ref)
	}
	read := func(name string) (string, error) {
		k, ok := sec.Keys[name]
		switch {
		case !ok:
			return "", fmt.Errorf("%s has no key %s", ref, name)
		case k.Kind != catalog.Issued:
			return "", fmt.Errorf("%s.%s is %s; an R2 token is issued", ref, name, k.Kind)
		case !live && k.RehearsalItem == "":
			return "", fmt.Errorf("%s.%s names no rehearsalItem: a rehearsal never reads a live token", ref, name)
		case !live:
			k = k.ForRehearsal()
		}
		v, err := vault.Issued(ctx, k)
		return string(v), err
	}
	id, err := read(s.AccessKey)
	if err != nil {
		return r2.Credentials{}, err
	}
	secret, err := read(s.SecretKey)
	if err != nil {
		return r2.Credentials{}, err
	}
	return r2.Credentials{AccessKeyID: id, SecretAccessKey: secret}, nil
}

// lockReader reads the live archive bucket's lock rules with the read-only
// account token.
func (a *app) lockReader(ctx context.Context, bw *bitwarden.Client, root string) (db.LockReader, error) {
	as, _, err := pgcluster.Stores(root, true)
	if err != nil {
		return nil, err
	}
	item, err := bw.Item(ctx, a.cfg.DB.LockTokenItem)
	if err != nil {
		return nil, err
	}
	token, err := item.Field(a.cfg.DB.LockTokenField)
	if err != nil {
		return nil, err
	}
	return r2.Locks{Account: as.Account(), Token: token}, nil
}

// rehearsalBucket opens the rehearsal archive bucket with its read-write
// rehearsal token, which cannot reach the live bucket.
func (a *app) rehearsalBucket(ctx context.Context, cmd *cobra.Command) (r2.Bucket, error) {
	if err := a.requireRehearsal("emptying the rehearsal bucket"); err != nil {
		return nil, err
	}
	root, cat, err := a.repository(ctx)
	if err != nil {
		return nil, err
	}
	archive, _, err := openStores(ctx, root, cat, secrets.NewBitwarden(bitwarden.New(cmd.ErrOrStderr())), false)
	return archive, err
}
