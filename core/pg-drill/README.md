# core/pg-drill — the Restore Drill

Once a month, restore the production backups from R2 into a throw-away
CloudNativePG Cluster, prove they are complete, read the mail back from them,
report, and delete it (ADR 0003, Amendments 2026-10-01, 2026-10-02 and
2026-11-02; runbook `docs/runbook/cluster-services.md` step 17).

| File | What it is |
|---|---|
| `namespace.yaml` | `pg-drill`. The drill's Cluster lives here and nowhere else |
| `rbac.yaml` | Create/delete its own Cluster, exec into its own instance; **read** the production Cluster and `Database` objects; create/delete the drill Stalwart and its Secret; **read** the production Stalwart Deployment |
| `objectstore.yaml` | The production bucket, seen with the **read-only** token |
| `cronjob.yaml` | 1st of the month, 09:00 UTC |
| `drill/drill.sh` | The drill. Its header lists the three assertions |
| `drill/cluster.yaml` | The throw-away Cluster: 1 instance, no WAL archiver, `hcloud-volumes` (Delete) |
| `drill/mail-pod.yaml` | The throw-away Stalwart: copies the mail bucket, applies the drill settings, reads the mail back |
| `drill/mail-settings.ndjson`, `mail-config.json`, `mail-apply.sh` | Its settings plan, its data store (the drill Cluster) and the recovery-mode apply |
| `drill/mail_check.py` | The read-back; `mail_check_test.py` beside it is its unit test (not in the ConfigMap) |
| `scw-mail-readonly.enc.yaml` | Scaleway ObjectStorageReadOnly on the Project `mail`: the bucket copy |
| `kustomization.yaml` | Puts `drill/` into a ConfigMap; lists everything except the namespace (applied by hand) and the Secrets |
| `r2-readonly-credentials.{yaml,enc.yaml}` | R2 Object Read, `tosak-pg-backups` + `tosak-drill-results` (the watcher's token) |
| `r2-drill-results-credentials.{yaml,enc.yaml}` | R2 Object Read & Write, `tosak-drill-results` only |
| `telegram.{yaml,enc.yaml}` | A copy of the infra bot token and chat id |

## What it proves, and what it does not

It proves the newest base backup restores, the WAL chain after it replays to
the newest segment in the bucket, and every database production declares
comes back with data.

The mail phase runs when production declares the database `mail` and the
three PostgreSQL assertions passed. PostgreSQL logs the time of the last
replayed commit when the replay ends; rclone copies `tosak-mail-blobs` as it
was at the next whole second (`--s3-version-at`) into the pod's `emptyDir`, so
the metadata and the message files describe one instant. A throw-away Stalwart
on production's image starts in recovery mode, applies
`drill/mail-settings.ndjson` (IMAP and HTTP on `127.0.0.1` only, the `local`
route only, the copy as a file blob store, no certificate, no metrics), then
starts normally on the restored `mail` database. `mail_check.py` logs in to
every account as `<account>%admin`, finds the canary message of
`drill@tosak.net` by its sha256 (`CANARY_SHA256` in `cronjob.yaml`) and
fetches 20 random messages, each of which must come back with a body of the
size the metadata records. The result is the `mail` field of the result
object: the canary, the counts, and the copy's files and bytes. The copy's
`emptyDir` is limited to 4 Gi; when `blobBytes` comes near it, the operator
decides how the drill goes on (ADR 0003).

🔴 The restored data store holds production's relay route and outgoing
queue. The drill Stalwart has no DNS (`dnsPolicy: None`; its only names are
the drill database and the Scaleway endpoint, as host aliases), no SES
credential, no relay route and no Service, and its blob store is the copy, so
a purge inside it deletes nothing in production. Flannel enforces no
NetworkPolicy, so these are the guard. Its passwords (its admin, and the
owner role in the throw-away copy) are made for each run and deleted with the
pod; production's never reach the drill.

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
cd tools/kluster   # the three Secrets; key names only, never values
go run . secrets apply pg-drill/r2-drill-results-credentials pg-drill/r2-readonly-credentials pg-drill/telegram pg-drill/scw-mail-readonly
go run . secrets apply --apply pg-drill/r2-drill-results-credentials pg-drill/r2-readonly-credentials pg-drill/telegram pg-drill/scw-mail-readonly
cd ../..
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

A failed mail phase names its step: the replay end time, the images, the
pod (with the failing container and the end of its log; the read-back's own
log is never shown) or the read-back's failures, by account id, mailbox
number and message number, never by address.

A drill Cluster or drill Stalwart left behind (the pod was killed hard) is
deleted by the next run's first step. While it exists its volume is attached, so the watcher does
not report it as an Orphaned Volume.
