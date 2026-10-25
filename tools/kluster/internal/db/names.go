package db

import "github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/pgcluster"

// The names db uses from the Cluster's files.
type (
	Generation = pgcluster.Generation
	Mode       = pgcluster.Mode
)

const (
	Fresh        = pgcluster.Fresh
	Recover      = pgcluster.Recover
	ClusterName  = pgcluster.ClusterName
	Namespace    = pgcluster.Namespace
	ArchiveStore = pgcluster.ArchiveStore
	SourceStore  = pgcluster.SourceStore
	ClusterFile  = pgcluster.ClusterFile
	RecoverPatch = pgcluster.RecoverPatch
)
