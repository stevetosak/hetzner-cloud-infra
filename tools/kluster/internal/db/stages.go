package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/local"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/pgcluster"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/r2"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/seed"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// Runbook is the page whose sections the Stages carry out.
const Runbook = "docs/runbook/pg-recovery.md"

// What the Cluster needs before it is applied. The names are the ones the
// ObjectStores and pg-cluster.yaml name.
var (
	// Deployments in cnpg-system: the operator and the barman-cloud plugin.
	needDeployments = []string{"cnpg-controller-manager", "barman-cloud"}
	// The Secrets every Cluster reads; a recovery reads the source token too.
	needSecrets       = []string{"db-credentials", "r2-backup-credentials"}
	needSourceSecrets = []string{"r2-source-credentials"}
)

// LockMin is the shortest lock a live recovery accepts on the generation it
// archives to (ADR 0003: the 30-day lock on the WAL).
const LockMin = 30 * 24 * time.Hour

// markerFile is CloudNativePG's recovery marker. An instance that still holds
// it refuses to archive into a non-empty path; after a switchover that stops
// archiving (plugin#828).
const markerFile = "/var/lib/postgresql/data/pgdata/.check-empty-wal-archive"

// FirstBackupPrefix names the base backup db create takes itself.
const FirstBackupPrefix = ClusterName + "-first-"

const adminEnv = "KUBECONFIG=" + kube.AdminConf + " "

// LockReader reads a bucket's lock rules.
type LockReader interface {
	Rules(ctx context.Context, bucket string) ([]r2.LockRule, error)
}

// run is what the Stages of one db create share.
type run struct {
	*Plan
	// sourceNewest is the newest segment of the source generation when the
	// preflight ran; a resumed run has none.
	sourceNewest *walFile
}

func kubectl(args ...string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = local.Quote(a)
	}
	return adminEnv + "kubectl " + strings.Join(q, " ")
}

// liveCluster is what the Stages read of the Cluster's status.
type liveCluster struct {
	Spec struct {
		Instances int `json:"instances"`
	} `json:"spec"`
	Status struct {
		Phase           string              `json:"phase"`
		ReadyInstances  int                 `json:"readyInstances"`
		CurrentPrimary  string              `json:"currentPrimary"`
		TargetPrimary   string              `json:"targetPrimary"`
		InstancesStatus map[string][]string `json:"instancesStatus"`
	} `json:"status"`
}

const healthyPhase = "Cluster in healthy state"

// getCluster returns the Cluster, or nil when there is none.
func getCluster(ctx context.Context, h *stage.Host) (*liveCluster, error) {
	out, err := h.Exec.Run(ctx, kubectl("get", "clusters.postgresql.cnpg.io", "-n", Namespace, ClusterName, "-o", "json", "--ignore-not-found"))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	var c liveCluster
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		return nil, fmt.Errorf("reading Cluster %s: %w", ClusterName, err)
	}
	return &c, nil
}

// psql runs one query on an instance and returns its unaligned output.
func psql(ctx context.Context, h *stage.Host, pod, query string) (string, error) {
	out, err := h.Exec.Run(ctx, kubectl("exec", "-n", Namespace, pod, "-c", "postgres", "--", "psql", "-XtAc", query))
	return strings.TrimSpace(out), err
}

// switchWAL closes the primary's current segment, so archiving shows at once
// instead of after archive_timeout.
func switchWAL(ctx context.Context, h *stage.Host) error {
	c, err := getCluster(ctx, h)
	if err != nil || c == nil || c.Status.CurrentPrimary == "" {
		return err
	}
	_, err = psql(ctx, h, c.Status.CurrentPrimary, "select pg_switch_wal()")
	return err
}

// --- db-preflight -----------------------------------------------------------

// Preflight is read only. Into an empty slot it proves what the Cluster needs
// is in place and the buckets agree with the repository; a Cluster that
// exists is refused unless it is this run's own render, unchanged (a resume).
type Preflight struct{ *run }

func (*Preflight) Name() string    { return "db-preflight" }
func (*Preflight) Runbook() string { return Runbook + "#db-preflight" }

func (p *Preflight) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	c, err := getCluster(ctx, h)
	if err != nil {
		return stage.Status{}, err
	}
	if c != nil {
		st, err := p.cluster.Probe(ctx, h)
		if err != nil {
			return stage.Status{}, err
		}
		if !st.Done {
			return stage.Status{}, fmt.Errorf("Cluster %s exists and is not the --%s render (%s): db create creates only into an empty slot and never changes or deletes a Cluster; deleting one is runbook step 2, by hand",
				ClusterName, p.Mode, st.Detail)
		}
		return stage.Status{Done: true, Detail: "resumed: the Cluster is this run's render, unchanged"}, nil
	}
	var errs []error
	errs = append(errs, needInCluster(ctx, h, p.Mode)...)

	g, next := p.Generation, p.Archives
	objs, err := p.Archive.List(ctx, next.Prefix())
	switch {
	case err != nil:
		errs = append(errs, err)
	case len(objs) > 0:
		errs = append(errs, fmt.Errorf("%s/%s holds %d object(s): an archive path is never written by two Clusters (CloudNativePG refuses a non-empty one)",
			p.Archive.Name(), next.Prefix(), len(objs)))
	}
	if p.Mode == Recover {
		errs = append(errs, p.checkSource(ctx)...)
		if p.Live {
			errs = append(errs, p.checkLock(ctx)...)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return stage.Status{}, err
	}
	detail := fmt.Sprintf("empty slot; %s/%s is empty", p.Archive.Name(), next.Prefix())
	if p.Mode == Recover {
		detail += fmt.Sprintf("; %s is the newest generation in %s", g, p.Source.Name())
		if p.sourceNewest != nil {
			detail += ", newest segment " + p.sourceNewest.Pos
		}
	}
	return stage.Status{Done: true, Detail: detail}, nil
}

// checkSource proves the repository's generation is the newest one with
// objects in the live bucket, and its next one is empty there.
func (p *Preflight) checkSource(ctx context.Context) []error {
	prefixes, err := p.Source.Prefixes(ctx)
	if err != nil {
		return []error{err}
	}
	var newest Generation
	for _, pre := range prefixes {
		if g, err := pgcluster.ParseGeneration(strings.TrimSuffix(pre, "/")); err == nil && g > newest {
			newest = g
		}
	}
	switch {
	case newest == 0:
		return []error{fmt.Errorf("%s holds no generation: there is nothing to recover from", p.Source.Name())}
	case newest != p.Generation:
		return []error{fmt.Errorf("the newest generation in %s is %s, the repository names %s: commit the generation of the last recovery first",
			p.Source.Name(), newest, p.Generation)}
	}
	w, err := readWALs(ctx, p.Source, p.Generation)
	if err != nil {
		return []error{err}
	}
	n, ok := w.newest()
	if !ok {
		return []error{fmt.Errorf("%s/%swals/ holds no segment", p.Source.Name(), p.Generation.Prefix())}
	}
	p.sourceNewest = &n
	return nil
}

func (p *Preflight) checkLock(ctx context.Context) []error {
	want := p.Archives.Prefix() + "wals/"
	rules, err := p.Locks.Rules(ctx, p.Archive.Name())
	if err != nil {
		return []error{err}
	}
	if _, ok := r2.Locked(rules, want, LockMin); !ok {
		return []error{fmt.Errorf("%s has no enabled lock rule of %d days or more on %s: add it in the Cloudflare dashboard before anything archives there; kluster never writes a lock",
			p.Archive.Name(), int(LockMin.Hours()/24), want)}
	}
	return nil
}

// needInCluster checks the operator, the plugin, the Secrets and the
// ObjectStores the Cluster names.
func needInCluster(ctx context.Context, h *stage.Host, mode Mode) []error {
	var errs []error
	out, err := h.Exec.Run(ctx, kubectl(append([]string{"get", "deployments", "-n", "cnpg-system", "-o", "json"}, needDeployments...)...))
	if err != nil {
		errs = append(errs, fmt.Errorf("the operator and the plugin: %w", err))
	} else {
		var list struct {
			Items []struct {
				Metadata struct{ Name string } `json:"metadata"`
				Status   struct {
					AvailableReplicas int `json:"availableReplicas"`
				} `json:"status"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(out), &list); err != nil {
			errs = append(errs, err)
		}
		for _, d := range list.Items {
			if d.Status.AvailableReplicas < 1 {
				errs = append(errs, fmt.Errorf("cnpg-system/%s has no available replica", d.Metadata.Name))
			}
		}
	}
	secrets := needSecrets
	stores := []string{ArchiveStore}
	if mode == Recover {
		secrets = append(slices.Clone(secrets), needSourceSecrets...)
		stores = append(stores, SourceStore)
	}
	// -o name only: the Secrets hold values.
	if _, err := h.Exec.Run(ctx, kubectl(append([]string{"get", "secrets", "-n", Namespace, "-o", "name"}, secrets...)...)); err != nil {
		errs = append(errs, fmt.Errorf("the Secrets %s in %s (kluster services seed makes them): %w", strings.Join(secrets, ", "), Namespace, err))
	}
	if _, err := h.Exec.Run(ctx, kubectl(append([]string{"get", "objectstores.barmancloud.cnpg.io", "-n", Namespace, "-o", "name"}, stores...)...)); err != nil {
		errs = append(errs, fmt.Errorf("the ObjectStores %s (core-cnpg syncs them): %w", strings.Join(stores, ", "), err))
	}
	return errs
}

func (p *Preflight) Preview(context.Context, *stage.Host) (string, error) {
	return "read only: the slot, the operator, the Secrets, the ObjectStores and the buckets\n", nil
}

func (*Preflight) Act(context.Context, *stage.Host) error {
	// Probe is done or refuses; there is nothing to do.
	return errors.New("db-preflight does not act")
}

// --- db-healthy ---------------------------------------------------------------

// Healthy waits until every instance is ready.
type Healthy struct{ *run }

func (*Healthy) Name() string    { return "db-healthy" }
func (*Healthy) Runbook() string { return Runbook + "#db-healthy" }

func (s *Healthy) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	c, err := getCluster(ctx, h)
	if err != nil || c == nil {
		return stage.Status{Detail: "no Cluster yet"}, err
	}
	d := fmt.Sprintf("%s, %d of %d ready", c.Status.Phase, c.Status.ReadyInstances, c.Spec.Instances)
	return stage.Status{Done: c.Status.Phase == healthyPhase && c.Status.ReadyInstances == c.Spec.Instances, Detail: d}, nil
}

func (s *Healthy) Preview(context.Context, *stage.Host) (string, error) {
	return fmt.Sprintf("wait up to %s for %d ready instances\n", s.Timeout, s.Instances), nil
}

func (s *Healthy) Act(ctx context.Context, h *stage.Host) error {
	_, err := s.Until(ctx, func() (stage.Status, error) { return s.Probe(ctx, h) })
	return err
}

// --- db-archive -------------------------------------------------------------

// Archive proves the Cluster archives into its generation. A recovery's
// first segment must be on a new timeline that follows the newest segment the
// preflight saw in the source, which proves replay reached the end of the WAL.
type Archive struct{ *run }

func (*Archive) Name() string    { return "db-archive" }
func (*Archive) Runbook() string { return Runbook + "#db-archive" }

func (s *Archive) Probe(ctx context.Context, _ *stage.Host) (stage.Status, error) {
	w, err := readWALs(ctx, s.Archive, s.Archives)
	if err != nil {
		return stage.Status{}, err
	}
	where := s.Archive.Name() + "/" + s.Archives.Prefix() + "wals/"
	if s.Mode == Fresh {
		if len(w.segments) == 0 {
			return stage.Status{Detail: "no segment in " + where}, nil
		}
		return stage.Status{Done: true, Detail: fmt.Sprintf("%d segment(s) in %s", len(w.segments), where)}, nil
	}
	if len(w.histories) == 0 {
		return stage.Status{Detail: "no timeline history in " + where}, nil
	}
	tl, _ := w.lowestTimeline()
	if len(w.segments) == 0 || tl == 0 {
		return stage.Status{Detail: "a timeline history, no segment yet, in " + where}, nil
	}
	first, _ := s.firstOfRecovery(w)
	d := fmt.Sprintf("timeline %d opened in %s", first.Timeline, first.Pos)
	switch {
	case s.TargetTime != nil:
		d += "; replay stopped at the target time, so the end of the WAL is not compared"
	case s.sourceNewest == nil:
		d += "; not compared with the source (a resumed run has no preflight reading)"
	case first.Pos < s.sourceNewest.Pos:
		return stage.Status{}, fmt.Errorf("%s, but the source's newest segment was %s: replay did not reach the end of the WAL", d, s.sourceNewest.Pos)
	default:
		// The new timeline opens in the segment that holds the end of
		// replay: the newest one when its last record ran past it, the
		// one after when it ended on a boundary (a WAL switch).
		d += ", at or after the source's newest segment " + s.sourceNewest.Pos
	}
	return stage.Status{Done: true, Detail: d}, nil
}

// firstOfRecovery is the first segment the recovered Cluster archived.
func (s *Archive) firstOfRecovery(w wals) (walFile, bool) {
	tl, ok := w.lowestTimeline()
	if !ok {
		return walFile{}, false
	}
	return w.firstOn(tl)
}

func (s *Archive) Preview(context.Context, *stage.Host) (string, error) {
	return fmt.Sprintf("switch WAL on the primary; wait for segments in %s/%swals/\n", s.Archive.Name(), s.Archives.Prefix()), nil
}

func (s *Archive) Act(ctx context.Context, h *stage.Host) error {
	if err := switchWAL(ctx, h); err != nil {
		return err
	}
	_, err := s.Until(ctx, func() (stage.Status, error) { return s.Probe(ctx, h) })
	return err
}

// --- db-marker --------------------------------------------------------------

// Marker proves no instance holds the recovery marker any more.
type Marker struct{ *run }

func (*Marker) Name() string    { return "db-marker" }
func (*Marker) Runbook() string { return Runbook + "#db-marker" }

func instancePods(ctx context.Context, h *stage.Host) ([]string, error) {
	out, err := h.Exec.Run(ctx, kubectl("get", "pods", "-n", Namespace, "-l", "cnpg.io/cluster="+ClusterName+",cnpg.io/podRole=instance",
		"-o", "jsonpath={.items[*].metadata.name}"))
	if err != nil {
		return nil, err
	}
	pods := strings.Fields(out)
	slices.Sort(pods)
	return pods, nil
}

// markers returns the instances that still hold the marker.
func markers(ctx context.Context, h *stage.Host) ([]string, int, error) {
	pods, err := instancePods(ctx, h)
	if err != nil {
		return nil, 0, err
	}
	var held []string
	for _, pod := range pods {
		out, err := h.Exec.Run(ctx, kubectl("exec", "-n", Namespace, pod, "-c", "postgres", "--",
			"sh", "-c", "if [ -e "+markerFile+" ]; then echo present; else echo absent; fi"))
		if err != nil {
			return nil, 0, err
		}
		if strings.TrimSpace(out) != "absent" {
			held = append(held, pod)
		}
	}
	return held, len(pods), nil
}

func (s *Marker) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	held, n, err := markers(ctx, h)
	switch {
	case err != nil:
		return stage.Status{}, err
	case n == 0:
		return stage.Status{Detail: "no instance yet"}, nil
	case len(held) > 0:
		return stage.Status{Detail: "the marker is still on " + strings.Join(held, ", ")}, nil
	}
	return stage.Status{Done: true, Detail: fmt.Sprintf("absent on all %d instance(s)", n)}, nil
}

func (s *Marker) Preview(context.Context, *stage.Host) (string, error) {
	return "wait until no instance holds " + markerFile + "\n", nil
}

func (s *Marker) Act(ctx context.Context, h *stage.Host) error {
	_, err := s.Until(ctx, func() (stage.Status, error) { return s.Probe(ctx, h) })
	return err
}

// --- db-replication ---------------------------------------------------------

// Replication proves every standby streams from the primary.
type Replication struct{ *run }

func (*Replication) Name() string    { return "db-replication" }
func (*Replication) Runbook() string { return Runbook + "#db-replication" }

func (s *Replication) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	c, err := getCluster(ctx, h)
	if err != nil || c == nil || c.Status.CurrentPrimary == "" {
		return stage.Status{Detail: "no primary yet"}, err
	}
	out, err := psql(ctx, h, c.Status.CurrentPrimary, "select count(*) from pg_stat_replication where state = 'streaming'")
	if err != nil {
		return stage.Status{}, err
	}
	n, err := strconv.Atoi(out)
	if err != nil {
		return stage.Status{}, fmt.Errorf("pg_stat_replication: %q", out)
	}
	want := s.Instances - 1
	return stage.Status{Done: n >= want, Detail: fmt.Sprintf("%d of %d standby(s) streaming from %s", n, want, c.Status.CurrentPrimary)}, nil
}

func (s *Replication) Preview(context.Context, *stage.Host) (string, error) {
	return fmt.Sprintf("wait until %d standby(s) stream (pg_stat_replication on the primary)\n", s.Instances-1), nil
}

func (s *Replication) Act(ctx context.Context, h *stage.Host) error {
	_, err := s.Until(ctx, func() (stage.Status, error) { return s.Probe(ctx, h) })
	return err
}

// --- db-switchover (a rehearsal only) ----------------------------------------

// Switchover proves archiving survives a switchover (plugin#828): a new
// primary, its timeline history and a segment on that timeline in the
// bucket, and no marker on any instance. A rehearsal only: live relies on
// db-marker, and a switchover there stops writes for a while.
type Switchover struct{ *run }

func (*Switchover) Name() string    { return "db-switchover" }
func (*Switchover) Runbook() string { return Runbook + "#db-switchover" }

// firstPrimary is the instance CloudNativePG bootstraps first.
const firstPrimary = ClusterName + "-1"

func (s *Switchover) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	c, err := getCluster(ctx, h)
	if err != nil || c == nil {
		return stage.Status{Detail: "no Cluster yet"}, err
	}
	if c.Status.CurrentPrimary == firstPrimary || c.Status.CurrentPrimary == "" {
		return stage.Status{Detail: "the primary is still " + firstPrimary}, nil
	}
	w, err := readWALs(ctx, s.Archive, s.Archives)
	if err != nil {
		return stage.Status{}, err
	}
	start, _ := w.lowestTimeline()
	tl := w.latestTimeline()
	if tl <= start {
		return stage.Status{Detail: fmt.Sprintf("primary %s; no timeline after %d in the bucket yet", c.Status.CurrentPrimary, start)}, nil
	}
	seg, ok := w.firstOn(tl)
	if !ok {
		return stage.Status{Detail: fmt.Sprintf("primary %s; timeline %d has no segment yet", c.Status.CurrentPrimary, tl)}, nil
	}
	held, _, err := markers(ctx, h)
	if err != nil {
		return stage.Status{}, err
	}
	if len(held) > 0 {
		return stage.Status{Detail: "the marker is on " + strings.Join(held, ", ")}, nil
	}
	return stage.Status{Done: true, Detail: fmt.Sprintf("primary %s archives timeline %d (%s); no marker", c.Status.CurrentPrimary, tl, seg.Pos)}, nil
}

func (s *Switchover) Preview(context.Context, *stage.Host) (string, error) {
	return "switch the primary to a standby (status.targetPrimary), switch WAL, wait for the new timeline in the bucket\n", nil
}

func (s *Switchover) Act(ctx context.Context, h *stage.Host) error {
	c, err := getCluster(ctx, h)
	if err != nil {
		return err
	}
	if c == nil {
		return errors.New("no Cluster")
	}
	target := ""
	for _, pod := range c.Status.InstancesStatus["healthy"] {
		if pod != c.Status.CurrentPrimary {
			target = pod
			break
		}
	}
	if c.Status.CurrentPrimary == firstPrimary {
		if target == "" {
			return errors.New("no healthy standby to switch to")
		}
		patch := fmt.Sprintf(`{"status":{"targetPrimary":%q}}`, target)
		if _, err := h.Exec.Run(ctx, kubectl("patch", "clusters.postgresql.cnpg.io", ClusterName, "-n", Namespace,
			"--type", "merge", "--subresource", "status", "-p", patch)); err != nil {
			return err
		}
		st, err := s.Until(ctx, func() (stage.Status, error) {
			c, err := getCluster(ctx, h)
			if err != nil || c == nil {
				return stage.Status{}, err
			}
			return stage.Status{Done: c.Status.CurrentPrimary == target && c.Status.Phase == healthyPhase, Detail: c.Status.Phase}, nil
		})
		if err != nil {
			return err
		}
		if !st.Done {
			return fmt.Errorf("the switchover to %s did not finish: %s", target, st.Detail)
		}
	}
	if err := switchWAL(ctx, h); err != nil {
		return err
	}
	_, err = s.Until(ctx, func() (stage.Status, error) { return s.Probe(ctx, h) })
	return err
}

// --- db-first-backup --------------------------------------------------------

// FirstBackup takes the first base backup of the generation, waits for it,
// and proves it by its files in the bucket (Amendment 2026-10-24: the
// ScheduledBackup has no `immediate`).
type FirstBackup struct{ *run }

func (*FirstBackup) Name() string    { return "db-first-backup" }
func (*FirstBackup) Runbook() string { return Runbook + "#db-first-backup" }

type backup struct {
	Name  string
	Phase string
	ID    string
	Error string
}

func firstBackups(ctx context.Context, h *stage.Host) ([]backup, error) {
	out, err := h.Exec.Run(ctx, kubectl("get", "backups.postgresql.cnpg.io", "-n", Namespace, "-o", "json"))
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct{ Name string } `json:"metadata"`
			Spec     struct {
				Cluster struct{ Name string } `json:"cluster"`
			} `json:"spec"`
			Status struct {
				Phase    string `json:"phase"`
				BackupID string `json:"backupId"`
				Error    string `json:"error"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("reading the Backups: %w", err)
	}
	var bs []backup
	for _, it := range list.Items {
		if strings.HasPrefix(it.Metadata.Name, FirstBackupPrefix) && it.Spec.Cluster.Name == ClusterName {
			bs = append(bs, backup{Name: it.Metadata.Name, Phase: it.Status.Phase, ID: it.Status.BackupID, Error: it.Status.Error})
		}
	}
	return bs, nil
}

func (s *FirstBackup) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	bs, err := firstBackups(ctx, h)
	if err != nil {
		return stage.Status{}, err
	}
	for _, b := range bs {
		switch b.Phase {
		case "completed":
			return s.inBucket(ctx, b)
		case "failed":
			return stage.Status{}, fmt.Errorf("Backup %s failed: %s; delete it and run again", b.Name, b.Error)
		}
	}
	if len(bs) > 0 {
		return stage.Status{Detail: fmt.Sprintf("Backup %s is %q", bs[0].Name, bs[0].Phase)}, nil
	}
	return stage.Status{Detail: "no first base backup"}, nil
}

// inBucket proves a completed backup by backup.info and data.tar.gz.
func (s *FirstBackup) inBucket(ctx context.Context, b backup) (stage.Status, error) {
	dir := s.Archives.Prefix() + "base/" + b.ID + "/"
	objs, err := s.Archive.List(ctx, dir)
	if err != nil {
		return stage.Status{}, err
	}
	have := map[string]int64{}
	for _, o := range objs {
		have[strings.TrimPrefix(o.Key, dir)] = o.Size
	}
	for _, f := range []string{"backup.info", "data.tar.gz"} {
		if _, ok := have[f]; !ok {
			return stage.Status{}, fmt.Errorf("Backup %s is completed, but %s/%s%s is missing", b.Name, s.Archive.Name(), dir, f)
		}
	}
	return stage.Status{Done: true, Detail: fmt.Sprintf("%s: %s with backup.info and data.tar.gz (%d bytes)", b.Name, dir, have["data.tar.gz"])}, nil
}

func (s *FirstBackup) Preview(context.Context, *stage.Host) (string, error) {
	return fmt.Sprintf("kubectl create Backup %s<time> (plugin); wait until completed; read %s/%sbase/<id>/\n",
		FirstBackupPrefix, s.Archive.Name(), s.Archives.Prefix()), nil
}

func (s *FirstBackup) Act(ctx context.Context, h *stage.Host) error {
	bs, err := firstBackups(ctx, h)
	if err != nil {
		return err
	}
	if len(bs) == 0 {
		name := FirstBackupPrefix + s.now().UTC().Format("20060102-150405")
		manifest := fmt.Sprintf(`apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata:
  name: %s
  namespace: %s
spec:
  cluster:
    name: %s
  method: plugin
  pluginConfiguration:
    name: barman-cloud.cloudnative-pg.io
`, name, Namespace, ClusterName)
		if _, err := h.Exec.Run(ctx, "mkdir -p -m 700 "+workDir); err != nil {
			return err
		}
		p := workDir + "/first-backup.yaml"
		if err := h.Exec.WriteFile(ctx, p, manifest, 0o600); err != nil {
			return err
		}
		if _, err := h.Exec.Run(ctx, adminEnv+"kubectl create --field-manager="+kube.FieldManager+" -f "+p); err != nil {
			return err
		}
	}
	_, err = s.Until(ctx, func() (stage.Status, error) { return s.Probe(ctx, h) })
	return err
}

// --- db-generation (a live recovery only) -------------------------------------

// GenerationBump writes the next generation into the repository once a live
// recovery has passed every other Stage. The next db create refuses until it
// is committed.
type GenerationBump struct{ *run }

func (*GenerationBump) Name() string    { return "db-generation" }
func (*GenerationBump) Runbook() string { return Runbook + "#db-generation" }

func (s *GenerationBump) Probe(context.Context, *stage.Host) (stage.Status, error) {
	g, err := pgcluster.Current(os.DirFS(s.Root))
	if err != nil {
		return stage.Status{}, err
	}
	if g == s.Archives {
		return stage.Status{Done: true, Detail: fmt.Sprintf("%s names %s; commit it: git add %s %s", ClusterFile, g, ClusterFile, RecoverPatch)}, nil
	}
	return stage.Status{Detail: fmt.Sprintf("%s still names %s", ClusterFile, g)}, nil
}

func (s *GenerationBump) Preview(context.Context, *stage.Host) (string, error) {
	return fmt.Sprintf("write %s → %s into %s and %s (commit it, with the lock rule for %swals/)\n",
		s.Generation, s.Archives, ClusterFile, RecoverPatch, s.Archives.Next().Prefix()), nil
}

func (s *GenerationBump) Act(context.Context, *stage.Host) error {
	_, err := pgcluster.Bump(s.Root)
	return err
}

// workDir holds the manifests db create applies on the Control Plane.
const workDir = "/var/lib/kluster/db"

// clusterStage applies the render as the seed applies what it owns.
func clusterStage(manifest []byte, mode Mode) *seed.Apply {
	return &seed.Apply{StageName: "db-cluster", Page: Runbook, Manifest: manifest,
		Target: seed.Target{Manager: seed.ManagerSeed}, What: "the --" + string(mode) + " render of Cluster " + ClusterName}
}
