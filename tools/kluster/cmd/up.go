package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/bitwarden"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/db"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/provision"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/seed"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/sshgate"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/tf"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

var upFlags struct {
	db string
}

func init() {
	f := upCmd.Flags()
	f.StringVar(&upFlags.db, "db", "", "fresh: an empty database; recover: restore the generation the repository names (required)")
	f.StringVar(&dbFlags.targetTime, "target-time", "", "--db recover: stop replay at this time, RFC 3339 in UTC (2026-10-24T10:00:00Z)")
	f.BoolVar(&dbFlags.rehearsed, "rehearsed", false, "live --target-time: the time was looked at in a rehearsal first; every later write is lost")
	f.DurationVar(&dbFlags.timeout, "timeout", 30*time.Minute, "how long each waiting Stage waits")
	_ = upCmd.MarkFlagRequired("db")
	rootCmd.AddCommand(upCmd)
}

var upCmd = &cobra.Command{
	Use:   "up --db fresh|recover",
	Short: "Build the whole cluster into an empty project: shared, cp init, every Worker, seed, db create, zone",
	Long: "Fills the empty slots of the environment and changes nothing that exists (ADR 0009, " +
		"Amendment 2026-10-25). It builds what the Worker set declares and never writes the set. " +
		"An up-preflight first runs every check that needs no cluster and prints every failure at " +
		"once: the working tree is the revision this environment's ArgoCD reads, the Worker set " +
		"declares Workers and no dead Worker exists, the Control Plane slot is empty or kluster's " +
		"own, every Bitwarden item is readable, and the buckets agree with the repository. Then one " +
		"bootstrap SSH gate covers cp init and every Worker, closed with an API readback before the " +
		"seed; the seed, db create and the zone check run on the Control Plane. A rerun resumes.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, func(ctx context.Context, a *app) error { return a.up(ctx, cmd) })
	},
}

// upPaths are what one up builds from. Each must be the revision the
// environment's ArgoCD reads, with no tracked change (ADR 0009, Amendment
// 2026-10-25, decision 5).
var upPaths = []string{"core", "projects", "infra", "secrets.yaml", "tools/kluster/kluster.yaml"}

func parseDBMode(s string) (db.Mode, error) {
	switch s {
	case string(db.Fresh):
		return db.Fresh, nil
	case string(db.Recover):
		return db.Recover, nil
	}
	return "", fmt.Errorf("--db %q: give fresh or recover", s)
}

// upRun is what the up-preflight learned and built, for the steps after it.
type upRun struct {
	set   *workerset.Set
	cpSrv *cloud.Server // nil: the project has no Control Plane
	// servers are the Worker servers that exist, by name.
	servers map[string]*cloud.Server
	synced  *syncedTree
	seed    *seed.Plan
	db      *db.Plan
}

func (r *upRun) close() {
	if r.synced != nil {
		r.synced.remove()
	}
}

func (a *app) up(ctx context.Context, cmd *cobra.Command) error {
	mode, err := parseDBMode(upFlags.db)
	if err != nil {
		return err
	}
	target, err := dbTargetTime(mode, a.env.IsLive())
	if err != nil {
		return err
	}
	a.warnOpenSSH(ctx)
	r, err := a.upPreflight(ctx, bitwarden.New(cmd.ErrOrStderr()), mode, target)
	if r != nil {
		defer r.close()
	}
	if err != nil {
		return err
	}

	shared, err := a.module(ctx, config.ModuleShared)
	if err != nil {
		return err
	}
	p, _, err := tf.Converge(ctx, shared, nil, sharedIntent, a.mode, a.out)
	if err != nil {
		return err
	}
	if !a.mode.Apply && len(p.Changes) > 0 {
		fmt.Fprintln(a.out, "Plan Mode: the Control Plane and the Workers plan once shared exists; what each would run:")
		return a.planUpFromEmpty(ctx, r)
	}
	if err := a.upBuild(ctx, r); err != nil {
		return err
	}
	return a.upServices(ctx, r)
}

// upPreflight runs every check that needs no cluster, all of them, and
// returns every failure at once. It also builds the seed and the db create,
// which reads the tokens they need.
func (a *app) upPreflight(ctx context.Context, bw *bitwarden.Client, mode db.Mode, target *time.Time) (*upRun, error) {
	fmt.Fprintln(a.out, "up-preflight: every check that needs no cluster")
	root, cat, err := a.repository(ctx)
	if err != nil {
		return nil, err
	}
	r := &upRun{}
	var errs []error
	add := func(e ...error) {
		for _, x := range e {
			if x != nil {
				errs = append(errs, x)
			}
		}
	}

	// One revision: what ArgoCD reads is what the working tree holds.
	if r.synced, err = a.exportSynced(ctx, root); err != nil {
		add(err)
	} else {
		add(a.checkRevision(ctx, gitRepo{root: root}, r.synced)...)
	}

	path, err := a.workerSetPath()
	if err != nil {
		return r, err
	}
	if r.set, err = workerset.Load(path); err != nil {
		add(err)
	} else if len(r.set.Names()) == 0 {
		add(fmt.Errorf("%s declares no Worker: kluster up builds what the Worker set declares and never writes it; kluster node add <name> declares one", path))
	}
	servers, err := a.api.Servers(ctx)
	if err != nil {
		return r, err
	}
	var serverErrs []error
	r.cpSrv, r.servers, serverErrs = sortServers(servers, a.cfg.ControlPlane.Name, r.set)
	add(serverErrs...)
	if r.cpSrv != nil {
		// kluster pins only servers it created.
		ours, err := a.pins.Has(r.cpSrv.PublicIP)
		add(err)
		if err == nil && !ours {
			add(fmt.Errorf("%s exists at %s and kluster did not create it: the Control Plane slot is not empty (ADR 0009)", r.cpSrv.Name, r.cpSrv.PublicIP))
		}
	}
	_, err = a.env.HcloudClusterToken()
	add(err)

	if r.synced != nil {
		if r.seed, err = a.seedPlan(ctx, bw, root, cat, r.synced.dir, dbFlags.timeout); err != nil {
			add(fmt.Errorf("the seed: %w", err))
		} else {
			add(r.seed.Readable(ctx)...)
		}
	}
	if r.db, err = a.dbPlan(ctx, bw, root, cat, mode, target, dbFlags.timeout); err != nil {
		add(fmt.Errorf("db create: %w", err))
	} else if r.cpSrv == nil {
		add(r.db.Buckets(ctx)...)
	} else {
		// A rerun's archive is not empty: db-preflight checks the buckets
		// with the Cluster in view.
		fmt.Fprintf(a.out, "up-preflight: %s exists, so db-preflight checks the buckets with the Cluster in view\n", r.cpSrv.Name)
	}

	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(a.out, "refused:", e)
		}
		return r, fmt.Errorf("up-preflight: %d problem(s); nothing was changed", len(errs))
	}
	fmt.Fprintln(a.out, "up-preflight: passed")
	return r, nil
}

// checkRevision refuses a HEAD that is not the commit ArgoCD reads, and a
// tracked change under upPaths. Untracked files do not count.
func (a *app) checkRevision(ctx context.Context, repo gitRepo, t *syncedTree) []error {
	var errs []error
	how := "merge it and check out " + t.rev
	if !a.env.IsLive() {
		how = "git push -f origin HEAD:" + t.rev
	}
	if t.head != t.commit {
		errs = append(errs, fmt.Errorf("HEAD is %s, but ArgoCD reads %s at %s: kluster up builds from one revision (%s)",
			t.head[:12], t.rev, t.commit[:12], how))
	}
	out, err := repo.run(ctx, append([]string{"status", "--porcelain", "--untracked-files=no", "--"}, upPaths...)...)
	if err != nil {
		return append(errs, err)
	}
	if d := strings.TrimSpace(string(out)); d != "" {
		errs = append(errs, fmt.Errorf("tracked changes that are not committed (commit them, then %s):\n%s", how, d))
	}
	return errs
}

// sortServers finds the Control Plane and the Worker servers, and refuses
// what kluster up does not build around (ADR 0009, Amendment 2026-10-25,
// decision 1): a server the Worker set does not declare, and a dead Worker,
// one that is not a Node of the cluster up builds. With no cluster to ask,
// a Worker is dead when no Control Plane exists, or when it is older than
// the Control Plane.
func sortServers(servers []*cloud.Server, cpName string, set *workerset.Set) (*cloud.Server, map[string]*cloud.Server, []error) {
	var cp *cloud.Server
	for _, s := range servers {
		if s.Name == cpName {
			cp = s
		}
	}
	workers := map[string]*cloud.Server{}
	var errs []error
	for _, s := range servers {
		if s == cp {
			continue
		}
		if set != nil {
			if _, ok := set.Get(s.Name); !ok {
				errs = append(errs, fmt.Errorf("server %s exists and the Worker set does not declare it: kluster up builds only what the set declares", s.Name))
				continue
			}
		}
		const way = "delete it by hand, or replace it with kluster node replace once the cluster exists"
		switch {
		case cp == nil:
			errs = append(errs, fmt.Errorf("Worker %s exists, but the project has no %s: it is a Worker of a lost cluster; %s", s.Name, cpName, way))
		case s.Created.Before(cp.Created):
			errs = append(errs, fmt.Errorf("Worker %s is older than %s: it is a Worker of an earlier cluster; %s", s.Name, cpName, way))
		default:
			workers[s.Name] = s
		}
	}
	return cp, workers, errs
}

// planUpFromEmpty prints, in Plan Mode, what every step would run in a
// project whose shared Module does not exist yet.
func (a *app) planUpFromEmpty(ctx context.Context, r *upRun) error {
	if err := a.planNewControlPlane(ctx); err != nil {
		return err
	}
	for _, w := range r.set.Workers() {
		if err := a.planNewWorker(ctx, w, a.unknownControlPlane(), nil); err != nil {
			return err
		}
	}
	return a.planServices(ctx, r)
}

// upBuild builds the Control Plane and every Worker behind one bootstrap
// SSH gate, and closes it with an API readback before it returns, failed
// runs included (ADR 0009, Amendment 2026-10-25, decision 8).
func (a *app) upBuild(ctx context.Context, r *upRun) (err error) {
	g, err := a.gate(ctx)
	if err != nil {
		return err
	}
	// The close runs whatever happens next (ADR 0009), an interrupt
	// included, so it gets a context the interrupt does not cancel.
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
		defer cancel()
		if cerr := g.Close(closeCtx); cerr != nil {
			err = errors.Join(err, cerr)
		}
	}()
	if err := g.Open(ctx, sshgate.ControlPlane, sshgate.Worker); err != nil {
		return err
	}

	cpSrv := r.cpSrv
	switch {
	case cpSrv == nil:
		if cpSrv, err = a.createControlPlane(ctx); err != nil {
			return err
		}
		if cpSrv == nil {
			// Plan Mode: printed what the build would run.
			return a.upWorkers(ctx, r, nil)
		}
	case !a.mode.Apply:
		fmt.Fprintf(a.out, "Plan Mode: %s exists; --apply resumes its build, every Stage whose Probe says it is not done\n", cpSrv.Name)
	}
	if a.mode.Apply {
		if err := a.buildControlPlane(ctx, cpSrv); err != nil {
			return err
		}
	}
	r.cpSrv = cpSrv
	return a.upWorkers(ctx, r, cpSrv)
}

// upWorkers creates every Worker the set declares that has no server, in
// one Terraform apply, then builds each Worker in turn (decision 9). cpSrv
// is nil in Plan Mode before the Control Plane exists.
func (a *app) upWorkers(ctx context.Context, r *upRun, cpSrv *cloud.Server) error {
	workers, err := a.module(ctx, config.ModuleWorkers)
	if err != nil {
		return err
	}
	var cp *stage.Host
	if cpSrv != nil {
		h, closeCP, err := a.reachControlPlane(ctx)
		if err != nil {
			return err
		}
		defer closeCP()
		cp = h
	}
	var missing []string
	for _, n := range r.set.Names() {
		if _, ok := r.servers[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		for _, n := range missing {
			if err := a.checkNoNode(ctx, cp, n); err != nil {
				return err
			}
		}
		created, err := provision.CreateWorkers(ctx, workers, missing, a.pins, a.mode, a.out)
		if err != nil {
			return err
		}
		if len(created) == 0 {
			plannedCP := cpSrv
			if plannedCP == nil {
				plannedCP = a.unknownControlPlane()
			}
			for _, n := range missing {
				w, _ := r.set.Get(n)
				if err := a.planNewWorker(ctx, w, plannedCP, cp); err != nil {
					return err
				}
			}
		}
	}
	if !a.mode.Apply {
		for n := range r.servers {
			fmt.Fprintf(a.out, "Plan Mode: %s exists; --apply resumes its build\n", n)
		}
		return nil
	}
	for _, w := range r.set.Workers() {
		srv, err := a.api.Server(ctx, w.Name)
		if err != nil {
			return err
		}
		if srv == nil {
			return fmt.Errorf("the Worker set declares %s, but the API has no such server after the create", w.Name)
		}
		if err := checkNewWorker(srv, w); err != nil {
			return err
		}
		fmt.Fprintf(a.out, "read back: %s id %s, %s, public %s, private %v\n", srv.Name, srv.ID, srv.Status, srv.PublicIP, srv.PrivateIPs)
		if err := a.buildWorker(ctx, w, srv, cpSrv, cp); err != nil {
			return err
		}
	}
	return nil
}

// upServices runs the seed, the database and the zone check on the Control
// Plane, the zone last (decision 3). A rehearsal reaches it through
// bootstrap SSH on its firewall, opened once more here; live through
// WireGuard.
func (a *app) upServices(ctx context.Context, r *upRun) error {
	if r.cpSrv == nil {
		return a.planServices(ctx, r)
	}
	return a.onControlPlane(ctx, func(cp *stage.Host) error {
		if cp == nil {
			return a.planServices(ctx, r)
		}
		opts := stage.Options{Apply: a.mode.Apply, Out: a.out}
		if err := stage.Run(ctx, r.seed.BeforeZone(), cp, opts); err != nil {
			return err
		}
		if err := a.upDB(ctx, r, cp, opts); err != nil {
			return err
		}
		return stage.Run(ctx, []stage.Stage{r.seed.Zone}, cp, opts)
	})
}

// upDB runs db create's Stages, unless the database step is already done by
// an earlier run whose generation bump is merged since (decision 7).
func (a *app) upDB(ctx context.Context, r *upRun, cp *stage.Host, opts stage.Options) error {
	built, detail, err := r.db.Built(ctx, cp)
	if err != nil && opts.Apply {
		return err
	}
	if built {
		fmt.Fprintf(a.out, "[%s] db: done (%s)\n", cp.Name, detail)
		return nil
	}
	err = stage.Run(ctx, r.db.Stages, cp, opts)
	if err == nil || opts.Apply {
		return err
	}
	// Plan Mode before the seed has run: the Cluster's kind is not there
	// to read yet.
	fmt.Fprintf(a.out, "[%s] the db Stages are not probed yet (%v); what they would run:\n", cp.Name, err)
	return stage.PlanNew(ctx, r.db.Stages, cp.Name, a.out)
}

// planServices prints what the seed, the database and the zone would run
// on a Control Plane that cannot be read yet.
func (a *app) planServices(ctx context.Context, r *upRun) error {
	all := append(append(r.seed.BeforeZone(), r.db.Stages...), r.seed.Zone)
	return stage.PlanNew(ctx, all, a.cfg.ControlPlane.Name, a.out)
}
