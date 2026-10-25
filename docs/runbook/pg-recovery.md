---
title: "PostgreSQL recovery runbook"
description: "How to bring tosak-pg-cluster back from the R2 backups with kluster db create after a total loss, a lost database, or logical damage, what each of its Stages proves, and how to rehearse it without touching production."
type: procedure
topics: [backups, databases, kubernetes]
verified: 2026-10-25
---

Run this when `tosak-pg-cluster` has to be rebuilt from the backups in R2. It
covers three cases, and they share one path:

| Case | What is gone | Recover to |
|---|---|---|
| Total loss | the whole Kubernetes cluster | the end of the WAL |
| Database lost | `tosak-pg-cluster` or its volumes; Kubernetes runs | the end of the WAL |
| Logical damage | nothing; data was dropped or corrupted by a mistake | a time before the mistake (PITR) |

The design is [ADR 0003](../adr/0003-recoverability.md), Amendments 2026-10-01
and 2026-10-02. Two facts from it shape every step. **The recovered cluster
keeps the name `tosak-pg-cluster`**, because every application addresses
`tosak-pg-cluster-rw`. And **it reads one archive generation and writes the
next**: CloudNativePG refuses to archive into a non-empty path, and the bucket
lock means the old path cannot be cleared. Today the cluster archives to
`tosak-pg-cluster-g1`, so a recovery reads `g1` and archives to `g2`.

The recovery manifest is not written by hand. It is the kustomize directory
[`core/cnpg/cluster/recover/`](../../core/cnpg/cluster/README.md): the fresh Cluster
[`core/cnpg/cluster/fresh/pg-cluster.yaml`](../../core/cnpg/cluster/fresh/pg-cluster.yaml)
with a JSON patch whose first operations `test` that the base still names `g1`.
It needs no cluster, so it works on the first day of a total loss. kluster
builds it with kustomize's own library, and a unit test proves the `test`
operations run (a base on `g2` fails the build). By hand the same render is
`kubectl kustomize core/cnpg/cluster/recover` (assumed: not run with this
layout).

## The way in: kluster

Since 2026-10-24 kluster does the work of steps 3 to 7 below, and proves each
of them as a Stage ([The stages of db create](#the-stages-of-db-create)).
Like every kluster write it runs in Plan Mode unless given `--apply`, and the
operator runs it in their own terminal, because it reads Bitwarden.

```sh
cd tools/kluster
go run . db create --recover                 # Plan Mode: every Stage and what it reads
go run . db create --recover --apply
go run . db create --recover --target-time 2026-10-04T10:00:00Z --rehearsed --apply   # logical damage on live
go run . up --db recover --apply             # total loss: the whole cluster, database included
```

`db create --fresh` is the first build of an empty database. `up --db
recover|fresh` builds shared, the Control Plane, the Workers, the seed and the
zone check around the same Stages ([ADR 0009, Amendment 2026-10-25](../adr/0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md#amendments)).
Both modes go into an empty slot only: they refuse while a Cluster
`tosak-pg-cluster` exists, and kluster never deletes one or any backup, so
step 2 stays a command by hand. `--target-time` takes RFC 3339 in UTC, and on
live it needs `--rehearsed`: a typed reminder that every later write is lost.
The steps below are what the Stages carry out, and the way back to a recovery
when kluster itself is not at hand.

## Before you start

- The base and the overlay name the same generation. Render once and read the
  `serverName` lines:

  ```bash
  kubectl kustomize core/cnpg/cluster/recover | grep serverName
  ```

  Expected: `tosak-pg-cluster-g1` (the source) and `tosak-pg-cluster-g2` (the
  archive). A failed `test` means the generations drifted; fix that first.
  `kluster services check` refuses an overlay whose `test` is not the base's
  `serverName` or whose replace is not the next generation.
- 🔴 **Add the R2 lock rule for the new generation before anything archives
  to it.** In the Cloudflare dashboard, bucket `tosak-pg-backups`, add a rule
  with prefix `tosak-pg-cluster-g2/wals/`, 30 days. A prefix is literal: the
  `g1` rule does not cover `g2`. Read the rule back from the dashboard. The
  rule for the next generation is added ahead of time, together with the bump
  commit of the recovery before it. kluster only reads the rules, with a
  read-only account token, and a live `--recover` refuses without an enabled
  rule of 30 days or more on the new generation's `wals/`. kluster never
  writes a lock. The outside watcher checks daily that rules
  of 30 days or more exist for the current and the next generation; it reads
  them with the repository secret `R2_CONFIG_READ_TOKEN`.
- The `tosak-pg-cluster-g2/` prefix is empty. The plugin checks this itself
  and refuses to archive into a non-empty path, but a leftover there means an
  earlier attempt ran; find out what it was before going on. kluster lists
  the bucket first and refuses unless `g1` is the newest generation with
  objects and `g2` is empty.
- **For logical damage, choose the target time first**, in UTC, before the
  mistake. Rehearse with it (see [Rehearsing](#rehearsing-without-touching-production))
  and look at the data in the rehearsal project before replacing production:
  every write after the target time is lost.

## Steps

Each step names the Stage that carries it out.

1. **Bring up what the Cluster needs.** After a total loss, rebuild the
   cluster and follow the apply order in
   [`core/cnpg/README.md`](../../core/cnpg/README.md) up to, not including,
   the Cluster — the CRDs, the namespaces, `db-credentials`,
   `r2-backup-credentials`, `r2-source-credentials`, and then the operator,
   the plugin, the Retain storage class and the two ObjectStores
   (`tosak-pg-backups` and the read-only `tosak-pg-backups-source`). The last
   ones are what `kubectl apply -k core/cnpg` applies and what the
   Application `core-cnpg` syncs once [`kluster services seed`](services-seed.md)
   has run. **Stop before the Cluster**: applying the fresh render would run
   `initdb` and archive an empty database. In the other two cases all of this
   is already in place. Stage: `db-preflight` checks it.

2. **Remove the old Cluster** (database lost, logical damage):

   ```bash
   kubectl delete cluster tosak-pg-cluster -n pg-cluster
   ```

   Its volumes are `Retain`: the PVCs go, the Hetzner volumes stay. Keep them
   until step 6 has passed (see [The old volumes](#the-old-volumes)). Writes
   stop here; the applications fail their readiness checks until step 4. No
   Stage does this: kluster never deletes a Cluster. The Cluster's `Backup`
   objects have no owner and stay behind, which `db-first-backup` allows for.

3. **Render and apply the recovery manifest.** Stage: `db-cluster`.

   ```bash
   kubectl kustomize core/cnpg/cluster/recover | kubectl apply -f -
   ```

   For logical damage, add the target between the two commands:

   ```bash
   kubectl kustomize core/cnpg/cluster/recover \
     | kubectl patch --local -f - --type json -o yaml -p \
       '[{"op":"add","path":"/spec/bootstrap/recovery/recoveryTarget","value":{"targetTime":"2026-10-04 10:00:00+00"}}]' \
     | kubectl apply -f -
   ```

   CloudNativePG picks the newest base backup before the target and replays
   the WAL up to it. `bootstrap.recovery` sets the `tosak` role's password
   from `db-credentials` again, so the applications' credentials keep working.

4. **Wait for the cluster.** Stages: `db-healthy`, `db-archive`, `db-marker`,
   `db-replication`.

   ```bash
   kubectl get cluster tosak-pg-cluster -n pg-cluster -w
   ```

   It goes from `Setting up primary` (the `full-recovery` job) to
   `Cluster in healthy state` with three ready instances. In the rehearsal
   the `full-recovery` job replayed 236 segments in about four minutes, and
   two instances were healthy six minutes after the apply.

   The job's log shows `archive command failed … The failed archive command
   was: false`, about once a second near its end. That is CloudNativePG
   holding archiving off on purpose until the real instance starts; the
   segments wait in `pg_wal` and are archived a few seconds later. It is not
   a fault, and `pg_stat_archiver.failed_count` on the new primary counts
   these failures too (12 in the rehearsal). The line that matters is
   `barman-cloud-check-wal-archive checking the first wal` near the start,
   followed by no error: the empty-archive check passed on `g2`.

5. **Take a base backup into the new generation** at once, so the new path
   has a restore point before the next 03:00 run. Stage: `db-first-backup`
   (kluster names it `tosak-pg-cluster-first-<time>`). The ScheduledBackup no
   longer has `immediate: true`, which left a Backup `pending` that started
   with the Cluster.

   ```bash
   kubectl create -f - <<'EOF'
   apiVersion: postgresql.cnpg.io/v1
   kind: Backup
   metadata:
     name: tosak-pg-cluster-after-recovery
     namespace: pg-cluster
   spec:
     cluster:
       name: tosak-pg-cluster
     method: plugin
     pluginConfiguration:
       name: barman-cloud.cloudnative-pg.io
   EOF
   ```

6. **Prove it** with the checks in [How to know it worked](#how-to-know-it-worked).
   Each Stage is one of them.

7. **Commit the new generation** on a branch. Stage: `db-generation`, a live
   recovery only, writes it: `serverName` in
   `core/cnpg/cluster/fresh/pg-cluster.yaml` becomes `tosak-pg-cluster-g2`
   (`bootstrap.initdb` stays; it only matters when a cluster is created
   empty), and the overlay's `test`, source and replace operations move up by
   one in the same commit. Add the lock rule for `g3` in the Cloudflare
   dashboard as well. Merge it by pull request: `kluster up` demands that
   `HEAD` is the commit ArgoCD reads, and the next `db create` refuses until
   the bump is committed. The outside watcher and the Restore Drill read
   `serverName` from this file and from the live Cluster, so they follow on
   their own once it merges.

## The stages of db create

`db create` runs these Stages in order. Each has a read-only Probe, and a Stage
whose Probe passes is skipped, so a run that stopped resumes at the first Stage
that is not done. kluster prints the anchor of the Stage that fails.

### db-preflight

Read only. It is done when the slot is empty and everything the Cluster needs
is in place, or when a Cluster exists and is exactly this run's render (a
resume). Into an empty slot it checks that the operator and the plugin
Deployments in `cnpg-system` have a replica, that the Secrets
(`db-credentials`, `r2-backup-credentials`, and for a recovery
`r2-source-credentials`) and the ObjectStores exist, that the archive
generation holds no object, and, for a recovery, that the generation the
repository names is the newest one with objects in the live bucket. A live
recovery also needs the lock rule of [Before you start](#before-you-start).
When it fails, the message names the check: commit the generation of the last
recovery, add the lock rule, run `kluster services seed`, or find out who wrote
into the archive path. A Cluster that differs from the render is refused,
because `db create` never changes or deletes a Cluster (step 2).

### db-cluster

Applies the render server-side with the field manager `kluster`. It is done when
the Cluster in the cluster equals the render.

### db-healthy

Done when the Cluster is in phase `Cluster in healthy state` and every instance
is ready (3 of 3). It waits up to `--timeout` (30 minutes by default). When it
times out, read `kubectl get cluster tosak-pg-cluster -n pg-cluster -w` and the
`full-recovery` job log; the repeated `archive command failed` lines near the
end of that job are expected (step 4).

### db-archive

Done when the Cluster archives into its generation. For a fresh Cluster that is
one segment under `<generation>/wals/`. For a recovery it is a timeline history
file and a segment on the new timeline, and that segment must be at or after the
newest segment the preflight saw in the source generation: that proves replay
reached the end of the WAL. A recovery with `--target-time` does not compare,
because replay stops at the target, and neither does a resumed run, which has no
preflight reading. The Stage first switches the WAL on the primary, so the
segment shows at once instead of after `archive_timeout`. When it fails with
"replay did not reach the end of the WAL", the recovery used less than the
bucket holds; do not go on.

### db-marker

Done when no instance holds `.check-empty-wal-archive`. An instance that still
holds it refuses to archive into a non-empty path, and after a switchover that
stops archiving for good (plugin#828).
This is the check live relies on, because a switchover on live stops writes for
a while.

### db-replication

Done when every standby streams: `pg_stat_replication` on the primary shows
`state = streaming` for each instance but the primary.

### db-switchover

A rehearsal only. kluster sets `status.targetPrimary` to a standby, waits for
the new primary and a healthy Cluster, switches the WAL, and waits for a new
timeline with a segment in the bucket and no marker on any instance. It is done
when archiving survives a switchover, which is the plugin#828 proof. Live skips
the Stage and relies on `db-marker`.

### db-first-backup

Creates the Backup `tosak-pg-cluster-first-<time>` with the plugin, waits for
phase `completed`, and is done when `base/<backupId>/backup.info` and
`data.tar.gz` are in the archive generation. A completed Backup without those
files is an error. Only Backups created after the current Cluster count,
because a deleted Cluster leaves its Backup objects behind. A `failed` Backup
stops the run: delete it and run again.

### db-generation

A live recovery only. Done when `core/cnpg/cluster/fresh/pg-cluster.yaml` and
the overlay name the new generation. kluster writes the files and prints `git
add` for them; commit them with the lock rule for the generation after, merged
by pull request. The next `db create` and `up` refuse until the bump is
committed.

## The old volumes

R2 holds everything up to the last archived WAL segment — at most
`archive_timeout` (300 s) behind the last write. The old primary's volume may
hold those last minutes in `pg_wal/`. That is the only reason to keep it.

The old PVs are `Released`, with `persistentVolumeReclaimPolicy: Retain`. To
read one, remove its `claimRef`, bind a PVC to it by `volumeName` in a scratch
namespace, and mount it **read-only** in a Pod running the production image.
`pg_wal/` segments newer than the newest object in
`tosak-pg-cluster-g1/wals/` are the unarchived tail. Using them means
uploading them into `g1` with `barman-cloud-wal-archive` **before** step 3,
because the recovered cluster starts a new timeline and cannot take them
later. This salvage path is not rehearsed.

After step 6 has passed and nothing is to be salvaged, delete the PVs and the
Hetzner volumes by hand. The outside watcher reports them as Orphaned Volumes
every day until then — that is intended.

## Rehearsing without touching production

A recovery is rehearsed in the rehearsal project, where it reads the live
backups with a read-only token and archives to a bucket of its own,
`tosak-pg-backups-rehearsal`, with a token that has no access to the live bucket
([ADR 0009, Amendment 2026-10-24](../adr/0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md#amendments)).
The rehearsal Cluster is the live Cluster, so the rehearsal proves the manifest
that will run. The scratch Cluster in a namespace on live, used for the first
rehearsal on 2026-10-03, is gone.

```sh
git push -f origin HEAD:rehearsal
go run . --env rehearsal up --db recover --apply
# a PITR rehearsal: --target-time 2026-10-04T10:00:00Z
```

[The rehearsal runbook](rehearsal.md#a-whole-rehearsal-with-kluster-up) has the
whole sequence, the data check and the teardown. `down --apply` empties the
rehearsal bucket after the project is empty and reads it back, and a rehearsal
never writes the generation bump. For logical damage, look at the data in the
rehearsal first and only then run the recovery on live with `--rehearsed`.

## How to know it worked

A `Cluster in healthy state` proves the restore, not the backups after it,
and `ContinuousArchiving=True` proves nothing at all (see step 15 of
[the cluster-services runbook](./cluster-services.md#15-backups--wal-archiving-and-a-daily-base-backup-to-r2)).
Each check below is made separately, and each is a Stage of `db create` since
2026-10-24. The evidence column is the first rehearsal, 2026-10-03, 22:18–22:30
UTC, in a scratch namespace on live. The Stages passed again in the rehearsal
project on 2026-10-24 (`--fresh` and `--recover`) and on 2026-10-25 (`up --db
fresh` and `up --db recover`; a new timeline opened after the live bucket's
newest segment, and the restored `doma` database had 33 tables, as live does).

| Check (Stage) | How | Rehearsal evidence |
|---|---|---|
| Restored to the end of the WAL (`db-archive`) | the primary's first archived segment is on a new timeline and follows the newest segment of `g1` | replay ended in `…0D000000A3`; timeline 2 opened in `…A4`, archived 22:22:38. Production archived its own `…A4` only at 22:26:20, so `…A3` was the newest in `g1` |
| The recovery marker is gone on every instance (`db-marker`) | `ls $PGDATA/.check-empty-wal-archive` in each instance's `postgres` container → `No such file or directory` | absent on both instances before the switchover and after it |
| The new generation receives WAL (`db-archive`) | `pg_stat_archiver` on the primary; `Archived WAL file` in the `plugin-barman-cloud` container's log; objects under `<serverName>/wals/` | `00000002.history` 22:22:37, then `…A4`–`…A8`, zstd |
| A standby joins (`db-replication`) | `select application_name, state from pg_stat_replication` on the primary | `rehearsal-2 \| streaming \| async` |
| Archiving survives a switchover (plugin#828; `db-switchover`, rehearsal only) | after the switchover, the new primary's archiver advances; no `Expected empty archive` in either log | `00000003.history` 22:26:28, `…A9.partial` 22:26:29; no `Expected empty archive` |
| A base backup lands in the new generation (`db-first-backup`) | the `Backup` is `completed`, and `base/<backupId>/` holds `backup.info` and `data.tar.gz` | `20261003T222702`, taken in 5 s on `rehearsal-1` (the former primary, now a standby); `data.tar.gz` 5.7 MB |

The switchover logged one refusal, `switchover in progress, refusing
archiving`, for `00000003.history`. That is CloudNativePG's own guard during
a switchover; the same file was archived two seconds later.

Two checks belong to a real recovery only and were not rehearsed:

- **The applications reconnect.** `authos-api` and `doma-web` pass their
  readiness checks against the recovered `tosak-pg-cluster-rw`.
- **The outside watcher is green on the new generation.** After step 7 has
  merged, dispatch it — `gh workflow run recoverability-watch.yml` — and
  expect a silent, green run. It reads `serverName` from `master`, finds the
  WAL and the base backup in `g2`, and so proves them from outside the
  cluster.

Only after both: the old volumes may go ([The old volumes](#the-old-volumes)).
