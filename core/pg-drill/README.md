# core/pg-drill — the Restore Drill

Once a month, restore the production backups from R2 into a throw-away
CloudNativePG Cluster, prove they are complete, report, and delete it
(ADR 0003, Amendments 2026-10-01 and 2026-10-02; runbook
`docs/runbook/cluster-services.md` step 17).

| File | What it is |
|---|---|
| `namespace.yaml` | `pg-drill`. The drill's Cluster lives here and nowhere else |
| `rbac.yaml` | Create/delete its own Cluster, exec into its own instance; **read** the production Cluster and `Database` objects |
| `objectstore.yaml` | The production bucket, seen with the **read-only** token |
| `cronjob.yaml` | 1st of the month, 09:00 UTC |
| `drill/drill.sh` | The drill. Its header lists the three assertions |
| `drill/cluster.yaml` | The throw-away Cluster: 1 instance, no WAL archiver, `hcloud-volumes` (Delete) |
| `kustomization.yaml` | Puts `drill/` into a ConfigMap; lists everything except the namespace (applied by hand) and the Secrets |
| `r2-readonly-credentials.{yaml,enc.yaml}` | R2 Object Read, `tosak-pg-backups` + `tosak-drill-results` (the watcher's token) |
| `r2-drill-results-credentials.{yaml,enc.yaml}` | R2 Object Read & Write, `tosak-drill-results` only |
| `telegram.{yaml,enc.yaml}` | A copy of the infra bot token and chat id |

## What it proves, and what it does not

It proves the newest base backup restores, the WAL chain after it replays to
the newest segment in the bucket, and every database production declares
comes back with data.

It does **not** prove a real recovery. The drill never archives, so it never
meets the next `serverName` generation, its lock rule, or plugin#828's
`.check-empty-wal-archive` marker.
[The PostgreSQL recovery runbook](../../docs/runbook/pg-recovery.md) owns
those, and its rehearsal proves them.

## 🔴 Its presence arms the outside watcher

`.github/workflows/recoverability-watch.yml` checks drill results only once
this directory exists on `master`. From then on, no `pass` result in 35 days
is an alert. Run the drill once by hand **before** this directory first
merges, or the next morning reports "no Restore Drill has ever passed".

## Apply

```sh
kubectl apply -f core/pg-drill/namespace.yaml
scripts/secrets.sh diff  core/pg-drill     # three new Secrets, values '***'
scripts/secrets.sh apply core/pg-drill
kubectl diff  -k core/pg-drill
kubectl apply -k core/pg-drill
```

Run it now, and follow it:

```sh
kubectl create job --from=cronjob/restore-drill -n pg-drill restore-drill-manual-$(date -u +%Y%m%d%H%M)
kubectl logs -n pg-drill -f job/<that name>
```

A run takes about as long as the restore — minutes on today's data. The
result object is `drill/<UTC timestamp>.json` in `tosak-drill-results`, not
one file per month, so a failed re-run cannot overwrite a month's pass.

## When it fails

The Telegram message names the step, and the Job's log has the rest. The
drill Cluster — and its instance logs — are already deleted by then. To look
at a failing restore, create the Cluster by hand from `drill/cluster.yaml`
with the two `set-by-drill.sh` values filled in, watch it, and delete it.

A drill Cluster left behind (the pod was killed hard) is deleted by the next
run's first step. While it exists its volume is attached, so the watcher does
not report it as an Orphaned Volume.
