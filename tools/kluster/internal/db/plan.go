package db

import (
	"errors"
	"os"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/pgcluster"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/r2"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/seed"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// Inputs is what one db create is built from.
type Inputs struct {
	Mode Mode
	Live bool
	// Root is the repository's top; the Cluster is rendered from its
	// working tree, which the caller has proven clean under ClusterDir.
	Root string
	// TargetTime stops a recovery's replay there (point in time).
	TargetTime *time.Time
	// Archive is the bucket this environment's Cluster archives to: the
	// live bucket, or the rehearsal bucket in a rehearsal.
	Archive r2.Bucket
	// Source is the live bucket through a read-only token; a recovery only.
	Source r2.Bucket
	// Locks reads the archive bucket's lock rules; a live recovery only.
	Locks LockReader
	seed.Waiting
	// Now is time.Now unless a test replaces it.
	Now func() time.Time
}

// Plan is one db create, built before the Control Plane is reached.
type Plan struct {
	Inputs
	// Generation is the one the repository names; Archives the one the
	// Cluster archives to (the next one for a recovery).
	Generation, Archives Generation
	Instances            int
	Manifest             []byte
	Stages               []stage.Stage

	cluster *seed.Apply
}

func (p *Plan) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Build renders the Cluster and lists the Stages (ADR 0009, Amendment
// 2026-10-24, decision 9).
func Build(in Inputs) (*Plan, error) {
	switch {
	case in.Mode != Fresh && in.Mode != Recover:
		return nil, errors.New("db create is --fresh or --recover")
	case in.Archive == nil:
		return nil, errors.New("no archive bucket")
	case in.Mode == Recover && in.Source == nil:
		return nil, errors.New("a recovery needs the source bucket")
	case in.Mode == Recover && in.Live && in.Locks == nil:
		return nil, errors.New("a live recovery reads the lock rules")
	}
	fsys := os.DirFS(in.Root)
	g, err := pgcluster.Current(fsys)
	if err != nil {
		return nil, err
	}
	if errs := pgcluster.CheckOverlay(fsys); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	n, err := pgcluster.Instances(fsys)
	if err != nil {
		return nil, err
	}
	m, err := pgcluster.Render(in.Root, in.Mode, in.TargetTime)
	if err != nil {
		return nil, err
	}
	p := &Plan{Inputs: in, Generation: g, Archives: g, Instances: n, Manifest: m, cluster: clusterStage(m, in.Mode)}
	if in.Mode == Recover {
		p.Archives = g.Next()
	}
	r := &run{Plan: p}
	p.Stages = []stage.Stage{&Preflight{r}, p.cluster, &Healthy{r}, &Archive{r}, &Marker{r}, &Replication{r}}
	if !in.Live {
		p.Stages = append(p.Stages, &Switchover{r})
	}
	p.Stages = append(p.Stages, &FirstBackup{r})
	if in.Live && in.Mode == Recover {
		p.Stages = append(p.Stages, &GenerationBump{r})
	}
	return p, nil
}
