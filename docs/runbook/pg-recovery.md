---
title: "PostgreSQL recovery runbook"
description: "How to bring tosak-pg-cluster back from the R2 backups after a total loss, a lost database, or logical damage, and how to rehearse it without touching production."
type: procedure
topics: [backups, databases, kubernetes]
verified: 2026-10-03
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

The recovery manifest is not written by hand. It is rendered from
[`core/cnpg/pg-cluster.yaml`](../../core/cnpg/pg-cluster.yaml) by the JSON
patches in [`core/cnpg/recovery/`](../../core/cnpg/recovery/README.md), with
`kubectl patch --local`. That needs no cluster and no tool beyond `kubectl`,
so it works on the first day of a total loss. The patch refuses to run unless
`pg-cluster.yaml` is still on `g1`.

## Before you start

- `pg-cluster.yaml` and the patches name the same generation. Render once and
  read the two `serverName` lines:

  ```bash
  kubectl patch --local -f core/cnpg/pg-cluster.yaml --type json \
    --patch-file core/cnpg/recovery/recover.json -o yaml | grep serverName
  ```

  Expected: `tosak-pg-cluster-g1` (the source) and `tosak-pg-cluster-g2` (the
  archive). `test failed` means the generations drifted; fix that first.
- 🔴 **Add the R2 lock rule for the new generation before anything archives
  to it.** In the Cloudflare dashboard, bucket `tosak-pg-backups`, add a rule
  with prefix `tosak-pg-cluster-g2/wals/`, 30 days. A prefix is literal: the
  `g1` rule does not cover `g2`. Read the rule back from the dashboard.
- The `tosak-pg-cluster-g2/` prefix is empty. The plugin checks this itself
  and refuses to archive into a non-empty path, but a leftover there means an
  earlier attempt ran; find out what it was before going on.
- **For logical damage, choose the target time first**, in UTC, before the
  mistake. Rehearse with it (see [Rehearsing](#rehearsing-without-touching-production))
  and look at the data in the rehearsal cluster before replacing production:
  every write after the target time is lost.

## Steps

1. **Bring up what the Cluster needs.** After a total loss, rebuild the
   cluster and follow the apply order in
   [`core/cnpg/README.md`](../../core/cnpg/README.md) up to and including
   `objectstore.yaml` — the operator, the plugin, the namespace, the Retain
   storage class, `db-credentials`, `r2-backup-credentials` and the
   ObjectStore. **Stop before `pg-cluster.yaml`**: applying it would run
   `initdb` and archive an empty database. In the other two cases all of this
   is already in place.

2. **Remove the old Cluster** (database lost, logical damage):

   ```bash
   kubectl delete cluster tosak-pg-cluster -n pg-cluster
   ```

   Its volumes are `Retain`: the PVCs go, the Hetzner volumes stay. Keep them
   until step 6 has passed (see [The old volumes](#the-old-volumes)). Writes
   stop here; the applications fail their readiness checks until step 4.

3. **Render and apply the recovery manifest.**

   ```bash
   kubectl patch --local -f core/cnpg/pg-cluster.yaml --type json \
     --patch-file core/cnpg/recovery/recover.json -o yaml \
     | kubectl apply -f -
   ```

   For logical damage, add the target between the two commands:

   ```bash
   kubectl patch --local -f core/cnpg/pg-cluster.yaml --type json \
     --patch-file core/cnpg/recovery/recover.json -o yaml \
     | kubectl patch --local -f - --type json -o yaml -p \
       '[{"op":"add","path":"/spec/bootstrap/recovery/recoveryTarget","value":{"targetTime":"2026-10-04 10:00:00+00"}}]' \
     | kubectl apply -f -
   ```

   CloudNativePG picks the newest base backup before the target and replays
   the WAL up to it. `bootstrap.recovery` sets the `tosak` role's password
   from `db-credentials` again, so the applications' credentials keep working.

4. **Wait for the cluster.**

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
   a fault. The line that matters is
   `barman-cloud-check-wal-archive checking the first wal` near the start,
   followed by no error: the empty-archive check passed on `g2`.

5. **Take a base backup into the new generation** at once, so the new path
   has a restore point before the next 03:00 run:

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

7. **Commit the new generation** on a branch: in `pg-cluster.yaml` change
   `serverName` to `tosak-pg-cluster-g2` and keep `bootstrap.initdb` as it
   is (it only matters when a cluster is created empty). Bump the generations
   in `core/cnpg/recovery/` in the same commit, as its README says. The
   outside watcher and the Restore Drill read `serverName` from this file and
   from the live Cluster, so they follow on their own once it merges.

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

The rehearsal runs the same patches, plus a second one that moves the result
into the namespace `pg-rehearsal`, names it `rehearsal`, gives it two small
instances on `hcloud-volumes` (Delete), and archives to
`tosak-pg-cluster-rehearsal` — a prefix with no lock rule, so it can be
cleared. `rehearsal.json` refuses anything but the `g2` render, so a broken
patch cannot archive into the real next generation.

The rehearsal costs two 10 GB volumes for a quarter of an hour and needs
about 512 MiB of free memory requests on the workers. It copies the R/W
backup token into the scratch namespace, because the rehearsal must archive;
the namespace is deleted at the end, and the Secret with it.

```bash
kubectl create namespace pg-rehearsal

# The R/W token, never printed. Compare hashes afterwards, not values.
kubectl get secret -n pg-cluster r2-backup-credentials -o json \
  | jq '{apiVersion,kind,type,data,metadata:{name:.metadata.name,namespace:"pg-rehearsal"}}' \
  | kubectl create -f -

kubectl patch --local -f core/cnpg/objectstore.yaml --type json \
  --patch-file core/cnpg/recovery/rehearsal-objectstore.json -o yaml \
  | kubectl create -f -

kubectl patch --local -f core/cnpg/pg-cluster.yaml --type json \
  --patch-file core/cnpg/recovery/recover.json -o yaml \
  | kubectl patch --local -f - --type json \
      --patch-file core/cnpg/recovery/rehearsal.json -o yaml \
  | kubectl create -f -
```

For a PITR rehearsal, put the `recoveryTarget` patch from step 3 between the
two patches. Once the cluster is healthy with two instances, run the checks
below, then a switchover and a base backup:

```bash
kubectl patch cluster rehearsal -n pg-rehearsal --type merge --subresource status \
  -p '{"status":{"targetPrimary":"rehearsal-2"}}'
kubectl wait cluster/rehearsal -n pg-rehearsal \
  --for=jsonpath='{.status.currentPrimary}'=rehearsal-2 --timeout=300s
# then a Backup as in step 5, named rehearsal-base, cluster rehearsal
```

Clearing up is done in this order. The Cluster goes first, so that nothing
archives into the prefix after it is cleared. The bucket is read and cleared
from a Pod in `pg-rehearsal` that runs the drill's pinned `alpine/k8s` image
with the copied token as `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`.

```bash
kubectl delete cluster rehearsal -n pg-rehearsal --wait=true
# in the Pod, with --endpoint-url set to the ObjectStore's endpointURL:
aws s3 rm --recursive --dryrun s3://tosak-pg-backups/tosak-pg-cluster-rehearsal/
#   every key must start with tosak-pg-cluster-rehearsal/ — then without --dryrun
aws s3api list-objects-v2 --bucket tosak-pg-backups --prefix tosak-pg-cluster-g2/ --query KeyCount
#   null: the rehearsal never wrote into the real next generation
kubectl delete namespace pg-rehearsal
```

🔴 The trailing slash and the full prefix matter. `tosak-pg-cluster-g1/base/`
is **not** locked (only its `wals/` is), so a delete with a wrong prefix can
erase production's base backups. The dry run is not optional.

Finally, list the Hetzner volumes: only production's three may remain. A
rehearsal volume that is still there a minute after the namespace is gone is
a stuck delete, which the outside watcher would report as an Orphaned Volume.

## How to know it worked

A `Cluster in healthy state` proves the restore, not the backups after it,
and `ContinuousArchiving=True` proves nothing at all (see step 15 of
[the cluster-services runbook](./cluster-services.md#15-backups--wal-archiving-and-a-daily-base-backup-to-r2)).
Each check below is made separately. The evidence column is the rehearsal of
2026-10-03, 22:18–22:30 UTC, in `pg-rehearsal`.

| Check | How | Rehearsal evidence |
|---|---|---|
| Restored to the end of the WAL | the primary's first archived segment is on a new timeline and follows the newest segment of `g1` | replay ended in `…0D000000A3`; timeline 2 opened in `…A4`, archived 22:22:38. Production archived its own `…A4` only at 22:26:20, so `…A3` was the newest in `g1` |
| The recovery marker is gone on every instance | `ls $PGDATA/.check-empty-wal-archive` in each instance's `postgres` container → `No such file or directory` | absent on both instances before the switchover and after it |
| The new generation receives WAL | `pg_stat_archiver` on the primary; `Archived WAL file` in the `plugin-barman-cloud` container's log; objects under `<serverName>/wals/` | `00000002.history` 22:22:37, then `…A4`–`…A8`, zstd |
| A standby joins | `select application_name, state from pg_stat_replication` on the primary | `rehearsal-2 \| streaming \| async` |
| Archiving survives a switchover (plugin#828) | after the switchover, the new primary's archiver advances; no `Expected empty archive` in either log | `00000003.history` 22:26:28, `…A9.partial` 22:26:29; no `Expected empty archive` |
| A base backup lands in the new generation | the `Backup` is `completed`, and `base/<backupId>/` holds `backup.info` and `data.tar.gz` | `20261003T222702`, taken in 5 s on `rehearsal-1` (the former primary, now a standby); `data.tar.gz` 5.7 MB |

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
