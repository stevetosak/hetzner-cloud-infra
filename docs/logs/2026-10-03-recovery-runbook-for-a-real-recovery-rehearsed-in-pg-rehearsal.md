---
title: "Recovery runbook for a real recovery, rehearsed in pg-rehearsal"
description: "How the PostgreSQL recovery patches were built and rehearsed against the real R2 archive, and what the rehearsal showed."
type: log
date: 2026-10-03
---

The [PostgreSQL recovery runbook](../runbook/pg-recovery.md) is the last
recovery item of Phase 6 in the rebuild Plan. It reads generation `g1` from R2
and archives the recovered cluster as `g2`. This Log records how the patches
were built and how they were rehearsed, and what the rehearsal found. The
runbook holds the procedure itself.

## Building the patches

The recovery manifest is not a second copy of the cluster. It is a set of JSON
patches applied to `core/cnpg/pg-cluster.yaml`, so the two cannot drift apart.
An offline probe proved the mechanism: `kubectl patch --local --type json
--patch-file` renders the manifest with `KUBECONFIG` pointing at a missing file,
so no server is involved, and `kubectl patch --local -f -` works in a pipe with
output identical to the file render (`diff` empty).

`core/cnpg/recovery/recover.json` tests the cluster name and `serverName` `g1`,
removes `initdb`, adds `bootstrap.recovery` (source `g1`, database `authos`,
owner `tosak`, secret `db-credentials`), adds `externalClusters` for `g1`, and
replaces `serverName` with `g2`. `rehearsal.json` starts with a test on `g2`,
renames the Cluster to `rehearsal` in namespace `pg-rehearsal`, sets two
instances on `hcloud-volumes` with 250m CPU and 256Mi memory, removes
`recovery.secret`, and sets `serverName` to `tosak-pg-cluster-rehearsal`. The
`bootstrap.recovery.{database,owner,secret}` fields were confirmed on the live
CRD with `kubectl explain`.

The first op of each patch is a `test` on the generation the patch reads. A
probe with the wrong generation (`g9`) ended with `error: testing value …
failed: test failed` and exit 1, so a stale patch stops instead of reading the
wrong archive.

## The rehearsal

Every mutation was approved separately by the operator. The rehearsal created
namespace `pg-rehearsal`, copied the R/W Secret (proven identical in both
namespaces by the sha256 of `.data`, `e3ef4777d136` in both), created the
ObjectStore without a retention policy, and applied the Cluster from the piped
patches.

Times are UTC, 2026-10-03. The Cluster was created at 22:18:21. The
full-recovery job completed at 22:22:31 after 236 "restored log file" lines, and
the Cluster was healthy with two instances at 22:24:07. A switchover through
`kubectl patch cluster rehearsal --subresource status -p
'{"status":{"targetPrimary":"rehearsal-2"}}'` finished at 22:26:28. A backup
`rehearsal-base` (method plugin) then completed on `rehearsal-1`, the former
primary and now a standby, between 22:27:02 and 22:27:07 (backupId
`20261003T222702`, beginWal and endWal `000000030000000D000000A9`).

A listing from a Pod `bucket-tool` (`alpine/k8s`, pinned digest, R/W token in
the environment) showed 11 objects under `tosak-pg-cluster-rehearsal/`: a
`backup.info` of 1436 B, a `data.tar.gz` of 5,749,186 B, and the WAL files
`00000002.history`, `…A4` to `…A8`, `A7.00000028.backup`, `A9.partial` and
`00000003.history`. The prefix `tosak-pg-cluster-g2/` had no objects
(`KeyCount` null), so the rehearsal wrote nowhere near the real generation.

The cleanup deleted the Cluster first, to stop archiving. A dry run listed 11
keys, none outside the prefix, and `aws s3 rm --recursive` removed those 11; the
prefix then showed `KeyCount` null and `g1/base/` still held 6 objects. The
namespace was deleted, the PVs were gone, and the Hetzner API listed only
volumes 106916161, 106916168 and 106916171, so the rehearsal volumes 107026552
and 107026580 were gone. Production was unchanged: healthy, 3 ready, primary
`tosak-pg-cluster-1`, timeline 1, last archived WAL `…0D000000A4` at 22:26:20.

## What the rehearsal found

The plugin#828 marker `.check-empty-wal-archive` was absent on `rehearsal-1` by
22:23, and on `rehearsal-2` before and after the switchover. No "Expected empty
archive" line appeared in either sidecar log, so the bug did not trigger in this
path.

`pg_stat_archiver.failed_count` read 12 on the recovered primary, with
`last_failed_wal` `00000002.history`. All of it came from the full-recovery job,
and the detail reads "The failed archive command was: false": CNPG holds
archiving off in that job. This is not a fault. The empty-archive check ran in
the job at 22:18:33 (`barman-cloud-check-wal-archive checking the first wal`)
with no error. One more failure, on `00000003.history` at 22:26:26, came from
the manager log "switchover in progress, refusing archiving"; the file was
archived at 22:26:28.

Replay ended in `…A3` and timeline 2 opened in `…A4`. Production's own `…A4` was
archived only at 22:26:20, so `…A3` was the newest WAL in `g1` at restore time.

🔴 `tosak-pg-cluster-g1/base/` is not locked, only `wals/` is (ADR 0003). A
recursive delete with a wrong prefix erases the production base backups. The
runbook makes the dry run mandatory for that reason.

Not rehearsed, and stated as such in the runbook: a point-in-time target, apps
reconnecting, the recoverability watcher on `g2`, and salvage of the old
`Retain` volumes.

## Links and the hook finding

The runbook is linked from step 15 of the cluster-services runbook (the
plugin#828 paragraph, with the "leaves open" item struck) and from its step 17
"leaves open", from `core/cnpg/README.md`, from `core/pg-drill/README.md`, and
from the rebuild Plan item. The Contract check passed, and a full `npm run
build` with `TOSAK_SOURCES_DIR` pointing at a symlink folder for the work tree
reported "All internal links are valid".

🔴 The cloud-infra pre-commit hook in a `.claude/worktrees/…` work tree looks for
tosak-docs at `<worktree>/../tosak-docs` and skips the Contract check ("no
tosak-docs clone at …/.claude/worktrees/tosak-docs"). The check was run by hand.
The hook path needs a small fix.

The next Plan line, "Only now: delete snapshot 349712331", is not done and
waits for the operator's approval.

## Lesson candidates

- [ ] **A patch that renders a recovery manifest starts with a `test` op on the generation it reads.** — Without it a stale patch reads the wrong archive silently; the `g9` probe ended with exit 1 and "test failed".
- [ ] **An archiver failure counter after a recovery is read with its failed command before it is called a fault.** — `failed_count` 12 looked like a broken archiver; the detail "The failed archive command was: false" came from the full-recovery job.
