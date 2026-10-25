# core/cnpg — PostgreSQL

PostgreSQL runs under **CloudNativePG**. One shared cluster,
`tosak-pg-cluster`, holds every project's database, owned by the `tosak` role
(ADR 0007).

| File | What it is |
|---|---|
| `render.sh` | Downloads the two pinned upstream releases, checks their sha256 and splits them |
| `crds.yaml` | The CRDs of the operator and the plugin, applied by the kluster seed only |
| `operator.yaml` | The operator, upstream `cnpg-1.30.0.yaml` without its CRDs and its Namespace |
| `namespace.yaml` | `cnpg-system` (the operator) and `pg-cluster` (the cluster) |
| `db-credentials.enc.yaml` | The `db-credentials` Secret, SOPS-encrypted. `secrets.yaml` at the top describes it |
| `cluster/` | The `Cluster`, built by `kluster db create` and never synced: `fresh/pg-cluster.yaml` (3 instances, 10Gi each on `hcloud-volumes-retain`, archiving through the plugin) and `recover/` (the same Cluster reading the previous generation). See `cluster/README.md` |
| `databases/<project>.yaml` | One `Database` per project database |
| `plugin-barman-cloud.yaml` | The barman-cloud CNPG-I plugin, upstream `v0.15.1` `manifest.yaml` without its CRD |
| `objectstore.yaml` | Where backups go: R2 bucket `tosak-pg-backups`, zstd WAL, 30-day retention. The only store a Cluster archives to |
| `source-objectstore.yaml` | The Source Store `tosak-pg-backups-source`: the same bucket, read only. Every recovery reads through it, in both environments |
| `r2-backup-credentials.enc.yaml` | The backup token (read and write), SOPS-encrypted. `secrets.yaml` at the top describes it |
| `r2-source-credentials.enc.yaml` | The read-only token the Source Store uses, SOPS-encrypted |
| `storageclass-retain.yaml` | `hcloud-volumes-retain` — a deleted PVC leaves its Hetzner volume behind |
| `scheduled-backup.yaml` | A full base backup every day at 03:00 UTC, from a standby. No `immediate`: `db create` takes the first base backup itself |

Backups, the bucket lock and why it covers only the WAL:
`docs/runbook/cluster-services.md` step 15 and ADR 0003, Amendments.

## Why a download and not a chart

**The CloudNativePG operator needs no values.** It watches every namespace by
default, its image is already pinned inside the released manifest, and nothing
in this cluster's design changes how it runs. A chart and a values file would
configure nothing.

The released manifests still cannot be used as they are. Only kluster applies
CRDs, and the seed owns every Namespace (ADR 0010, Amendment 2026-10-22), so
`render.sh` downloads both releases, checks each against its pinned sha256 and
splits them: the CRDs to `crds.yaml`, the `cnpg-system` Namespace to
`namespace.yaml`, the rest unchanged. The header of each file names its source.
To upgrade, change a version and its sha256 in `render.sh`, run it, and read
the diff.

🔴 **Before upgrading the plugin, check which barman it ships**
(`containers/sidecar-requirements.txt` at the tag). Any barman that rewrites
an object it already uploaded fails against the bucket lock — that is why the
lock covers only `wals/` today.

## Apply

Order matters. Everything `pg-cluster.yaml` names must exist before it: the
operator's CRDs, the `db-credentials` Secret, the Retain StorageClass (or the
first volumes are created `Delete`), and the plugin with its `ObjectStore` and
token (or the instances have no archiver). The plugin needs cert-manager.

```sh
kubectl apply --server-side --field-manager=cloud-infra -f core/cnpg/crds.yaml
kubectl apply -f core/cnpg/namespace.yaml
# the two Secrets (from tools/kluster; drop --apply for the diff first)
#   go run . secrets apply --apply pg-cluster/db-credentials pg-cluster/r2-backup-credentials
# the operator, the plugin, the StorageClass, both ObjectStores, the
# ScheduledBackup and doma's Database: what the Application core-cnpg syncs.
# Run it again if it fails on a webhook: the operator starts first.
kubectl apply --server-side --field-manager=cloud-infra -k core/cnpg
# the Cluster: not by hand, and never by ArgoCD
#   go run . db create --fresh --apply         (from tools/kluster)
```

`kluster db create` builds `cluster/fresh` or `cluster/recover`, applies it into
an empty slot and proves it healthy, archiving and backed up. It also needs
`r2-source-credentials` for a recovery. After a real recovery the Archive
Generation moves up: kluster writes the bump into `cluster/` (the `serverName`
and the overlay), and the next `db create` refuses until it is committed, with
the lock rule for the generation after added in the Cloudflare dashboard.
Follow [the PostgreSQL recovery runbook](../../docs/runbook/pg-recovery.md).

🔴 **`--server-side` is not optional for `crds.yaml`**, for the same reason
as the Gateway API CRDs. The `clusters.postgresql.cnpg.io` CRD alone is about
7,700 lines; a client-side apply writes the whole object into the
`last-applied-configuration` annotation and overflows the 256 KiB annotation
limit.

## Scope

**`authos` and `doma` only** (ADR 0003). `authos` is not a `Database` object —
it is created by `bootstrap.initdb.database` when the cluster first starts.

`databases/imaps.yaml` and `databases/wasteio.yaml` are committed but **must
not be applied**. Those projects are inactive; their manifests stay in the
repository unsynced.

## Backups

Since 2026-10-02, WAL is archived continuously and a full base backup is taken
every day to R2 bucket `tosak-pg-backups`, path `tosak-pg-cluster-g1/`, kept
30 days (ADR 0003 Amendments; `docs/runbook/cluster-services.md` step 15).
Git is the record of the current Archive Generation: it is the `serverName` in
`cluster/fresh/pg-cluster.yaml`. A rehearsal archives to its own bucket
`tosak-pg-backups-rehearsal` (`core/rehearsal/cnpg`), never to this one.

**A backup is not proven until a restore is.** The monthly Restore Drill
(`core/pg-drill/`) restores from this bucket, and a rehearsal recovery has
restored it into the rehearsal project (2026-10-24, 2026-10-25).
