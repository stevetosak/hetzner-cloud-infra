# core/cnpg — PostgreSQL

PostgreSQL runs under **CloudNativePG**. One shared cluster,
`tosak-pg-cluster`, holds every project's database, owned by the `tosak` role
(ADR 0007).

| File | What it is |
|---|---|
| `operator.yaml` | The operator, upstream `cnpg-1.30.0.yaml`, byte for byte |
| `namespace.yaml` | The `pg-cluster` namespace, which holds the cluster, not the operator |
| `credentials.yaml` | Template for the `db-credentials` Secret. Values are never committed |
| `pg-cluster.yaml` | The `Cluster` — 3 instances, 10Gi each on `hcloud-volumes-retain`, archiving through the plugin |
| `databases/<project>.yaml` | One `Database` per project database |
| `plugin-barman-cloud.yaml` | The barman-cloud CNPG-I plugin, upstream `v0.15.1` `manifest.yaml`, byte for byte |
| `objectstore.yaml` | Where backups go: R2 bucket `tosak-pg-backups`, zstd WAL, 30-day retention |
| `r2-backup-credentials.yaml` | Template for the backup token. The real Secret is `r2-backup-credentials.enc.yaml` (SOPS) |
| `storageclass-retain.yaml` | `hcloud-volumes-retain` — a deleted PVC leaves its Hetzner volume behind |
| `scheduled-backup.yaml` | A full base backup every day at 03:00 UTC, from a standby |

Backups, the bucket lock and why it covers only the WAL:
`docs/runbook/cluster-services.md` step 15 and ADR 0003, Amendments.

## Why this is not rendered from a chart

`core/gateway` and `core/cert-manager` carry a `render.sh`, because both need
values set — Envoy Gateway must have the experimental CRDs stripped, and
cert-manager must be told to enable Gateway API support.

**The CloudNativePG operator needs no values.** It watches every namespace by
default, its image is already pinned inside the released manifest, and nothing
in this cluster's design changes how it runs. A chart, a values file and a
render script would add three files that configure nothing.

So `operator.yaml` follows the other pattern in this repository, the one
`core/gateway-api/crds.yaml` and `core/cni/flannel.yaml` use: a named upstream
release, copied unmodified. To upgrade, replace the file from the new release
and change the version named here.

Upstream source, so the copy can be checked at any time:

```
https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.30/releases/cnpg-1.30.0.yaml
https://github.com/cloudnative-pg/plugin-barman-cloud/releases/download/v0.15.1/manifest.yaml
  sha256 8f377ff4a7d0d3f74fc64b0f7ac4f449fc38ca9e83a40107cca2f9315a9bd920
```

🔴 **Before upgrading the plugin, check which barman it ships**
(`containers/sidecar-requirements.txt` at the tag). Any barman that rewrites
an object it already uploaded fails against the bucket lock — that is why the
lock covers only `wals/` today.

## Apply

Order matters. The operator's CRDs must exist before any `Cluster` object, and
the Secret must exist before the cluster bootstraps.

```sh
kubectl apply --server-side --field-manager=cloud-infra -f core/cnpg/operator.yaml
kubectl apply -f core/cnpg/namespace.yaml
# then create the db-credentials Secret — see credentials.yaml
kubectl apply -f core/cnpg/pg-cluster.yaml
kubectl apply -f core/cnpg/databases/doma.yaml
```

Backups, after the cluster exists (the plugin needs cert-manager):

```sh
scripts/secrets.sh apply core/cnpg/r2-backup-credentials.enc.yaml
kubectl apply --server-side --field-manager=cloud-infra -f core/cnpg/plugin-barman-cloud.yaml
kubectl apply -f core/cnpg/storageclass-retain.yaml
kubectl apply -f core/cnpg/objectstore.yaml
kubectl apply -f core/cnpg/pg-cluster.yaml
kubectl apply -f core/cnpg/scheduled-backup.yaml
```

On a fresh build, apply `storageclass-retain.yaml` **before** the first
`pg-cluster.yaml`, so the first volumes are created `Retain` and need no
patch.

🔴 **`--server-side` is not optional for `operator.yaml`**, for the same reason
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

## Backups are Phase 6, and until then there are none

ADR 0003 decides PostgreSQL PITR to R2 with barman-cloud: continuous WAL
archiving, a daily base backup, 30-day retention, and a monthly restore drill.
**None of it is here yet** — this manifest has no `backup` stanza, which is the
exact gap that made the 2026-09-13 loss unrecoverable.

That is deliberate sequencing, not an oversight: the checklist puts it in
Phase 6. Note also that CloudNativePG has moved barman-cloud support out of the
operator and into a plugin, so Phase 6 is a plugin install, not a stanza.

**Treat every database here as disposable until Phase 6 closes.**
