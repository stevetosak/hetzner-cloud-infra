# 3. The cluster is reconstructible from this repo plus one bucket

Date: 2026-09-20

## Status

Accepted

## Context

Total loss of the cluster cost four PostgreSQL databases, every Longhorn
volume, and every cluster object that existed only in etcd. There were no
backups of any kind: the CNPG cluster had no `backup` stanza, Longhorn had no
backup target, and nothing snapshotted etcd.

The rebuild also exposed how much of the cluster lived nowhere durable:

- Five of eight ArgoCD Applications were created by hand in the web UI and
  existed in no file.
- Every Secret was applied out of band. `credentials.yaml` files are committed
  as blank-valued templates that document shape but hold nothing.
  `projects/doma/scripts/init_secrets.sh` recovers field values by reading them
  out of the live cluster — the one place they are guaranteed absent during a
  recovery.
- The CNI, the Hetzner CCM, and every operator install were undocumented. See
  ADR 0001.

## Decision

Make the repository plus one Cloudflare R2 bucket sufficient to rebuild
everything, and verify that claim on a schedule.

**Declaration — one ApplicationSet.** Replace hand-made Applications with a
single ApplicationSet using a git directory generator over the project overlay
paths. Adopted during the rebuild specifically because every hazard that
deferred this migration — adopting Applications without ownerRefs, the
`waste-bin-agent` directory versus the live `wasteio-bin-agents` name, the glob
swallowing `projects/imaps/**` — exists only when migrating a *running*
cluster. Against an empty cluster the migration is free. Deferring it again
means paying that cost later.

**Scope — active projects only.** `authos` and `doma` are restored. `wasteio`
and `imaps` manifests stay in the repo but are not synced; both projects have
moved to `Projects/Inactive`. This also removes the mqtt service from the load
balancer.

**Secrets — SOPS with age.** Each `credentials.yaml` template gains a committed
`credentials.enc.yaml` encrypted with age. Restore is
`sops -d … | kubectl apply -f -`, which matches the existing out-of-band apply
pattern and therefore needs no ArgoCD plugin. The age private key lives in a
password manager, outside both the cluster and the repo.

Chosen over Sealed Secrets deliberately: Sealed Secrets keeps its sealing key
*inside* the cluster, so a total cluster loss also destroys the ability to
decrypt the secrets committed to git. That is this incident's failure mode
applied to its own remedy.

**Data — PostgreSQL PITR to R2.** CNPG barman-cloud with continuous WAL
archiving plus a daily base backup, 30-day retention. PostgreSQL is scoped as
the only data git cannot reconstruct.

**Verification — a restore drill.** A scheduled job restores the latest backup
into a throwaway CNPG cluster, asserts row counts, and fails loudly. Monthly.

**Deliberately not backed up:** etcd, and Longhorn volumes other than
PostgreSQL. After the decisions above, etcd holds little that git does not, and
the only other persistent volume is Prometheus history.

## Consequences

- Losing every server again costs the Prometheus history and whatever writes
  fall inside the WAL archive gap. Everything else is a rebuild, not a loss.
- The age private key becomes a single point of failure for every secret. Losing
  it is unrecoverable, and it must be stored with that in mind.
- The restore drill costs a throwaway CNPG cluster's resources monthly, and it
  will occasionally fail for its own reasons rather than the backup's. That
  noise is the price of the signal.
- Skipping etcd backups means cert-manager re-issues all certificates on a
  rebuild. Let's Encrypt permits 5 duplicate certificates per week, which is
  ample for four hostnames but is a real ceiling during repeated rebuilds.
- Reviving wasteio or imaps later means re-adding their overlay directories and
  provisioning their databases and secrets afresh.

## Amendments

**2026-09-22 — SOPS is pulled forward from Phase 5 and starts with authos.**
The per-project `init_secrets.sh` scripts were hand-written prompt code, one
per project, and each new Secret meant new code. authos needed a rewrite of
its script, which Phase 5 would have deleted. So SOPS starts now; doma,
`redis` and `db-credentials` move over later. What building it settled:

- **One format for every Secret, including binary ones.** Each Secret is a
  whole Kubernetes manifest, `<name>.enc.yaml`, beside its blank template
  `<name>.yaml` — not only `credentials.enc.yaml`. The authos keystore is a
  Secret manifest with `data.keystore.p12`, so a PKCS#12 file and a password
  take the same path.
- **Only `data` and `stringData` are encrypted** (`.sops.yaml`), so the name,
  namespace and key names stay readable in review.
- **One entry point, `scripts/secrets.sh`**, with `edit`, `encrypt`, `apply`
  and `check`. `encrypt` reads a manifest on stdin, so a value that already
  lives in a file or in the cluster never touches disk in plaintext.
- **`.githooks/pre-commit` refuses an unencrypted `*.enc.yaml`**, reading the
  index rather than the working tree. It must be enabled once per clone with
  `git config core.hooksPath .githooks`.
- 🔴 **This repository is public, and that has a cost this ADR did not state.**
  The ciphertext is published for ever. If the age private key ever leaks,
  every value in the history decrypts, and rotating the live Secret does not
  protect the old commits. The signing keystore is the worst case: a leak of
  the age key is a leak of Authos's signing key. Keep the age key in the
  password manager and nowhere else durable.

**2026-10-01 — the backup design, settled before building it.** CNPG 1.30
no longer carries barman-cloud in the operator; it is the barman-cloud CNPG-I
plugin plus an `ObjectStore`. What was decided:

- **A separate bucket, `tosak-pg-backups`, with its own R2 API token scoped to
  that bucket only.** Not a prefix in `hetzner-cloud-infra`. The terraform
  token lives on the operator's laptop; the backup token lives in the cluster,
  read by a sidecar in every PostgreSQL pod. A token that could reach the
  terraform state would let a compromised cluster read or corrupt it. A
  separate bucket also keeps lifecycle and lock rules off the state objects.
- **The backup token is SOPS-encrypted from its first apply**, as
  `core/cnpg/r2-backup-credentials.enc.yaml` beside a blank template, and the
  operator keeps a second copy in the password manager. After a total loss the
  restore needs this token before anything else; a token held only in etcd
  would die with the cluster it is meant to recover. The recovery path is
  password manager → age key → git → token → bucket, and no step depends on
  the cluster. `scripts/secrets.sh` and `.sops.yaml` already accept any path,
  so `core/` needs no change to either.
- **A 30-day bucket lock rule, no prefix, from the first backup.** R2's Object
  Read & Write permission includes delete, and that token lives in the
  cluster, so without a lock the cluster could erase its own history. The
  in-cluster token cannot edit bucket configuration; only the operator can
  remove the rule, from the dashboard. The lock does not fight retention: the
  plugin's 30-day recovery window deletes only objects older than the newest
  backup before now − 30 days, which are always outside the lock. Cost: no
  backup can be deleted inside 30 days, so tests write to another
  `serverName` path, and the bucket cannot be emptied until the rule is
  removed.
- **RPO about 5 minutes**: CNPG's own `archive_timeout` of 300 s, unchanged.
  On these quiet databases the timer, not the 16 MB segment size, decides when
  WAL leaves the cluster. **WAL compression `zstd`** (fall back to `gzip` only
  if the plugin's barman rejects it): a segment forced by the timer is 16 MB
  and nearly empty, and uncompressed that is up to 4.6 GB a day.
- **A daily full base backup at 03:00 UTC, from a standby**
  (`target: prefer-standby`, written out though it is the default), with
  `immediate: true` so the first one does not wait for the schedule.
  barman-cloud has no incremental base backups and the Hetzner CSI driver has
  no snapshots, so each base backup is a full copy — about 30 copies of ~26 MB
  over the window. Daily is chosen for a short WAL chain at restore, not for
  the RPO, which WAL sets. Move to weekly if the data grows to many GB.
- **PostgreSQL volumes are `Retain`, not provider delete-protected.** A new
  StorageClass `hcloud-volumes-retain` (not the default) for every future
  instance PVC, and the three existing PVs patched to
  `persistentVolumeReclaimPolicy: Retain` — a StorageClass's policy is copied
  into a PV only when the PV is created. Chosen over Hetzner
  `protection.delete`, which makes the CSI `DeleteVolume` call fail and retry
  for ever, and which guards the console, not the real risk: a deletion
  through Kubernetes. Cost: an orphaned volume stays and is billed until a
  person deletes it; and `pg-cluster.yaml` names `hcloud-volumes-retain` while
  the three current PVCs still show `hcloud-volumes`. That mismatch is
  correct — CloudNativePG does not migrate existing PVCs.
- **Orphaned volumes are reported, never deleted by automation**, with a
  read-only Hetzner token. An orphan made by `Retain` is the copy `Retain`
  exists to keep; deleting it after a grace period brings back the loss with
  a delay. A Hetzner token is scoped to the whole project, so a token able to
  delete a volume could delete the servers and the load balancer too. The
  report carries the exact delete command; a person runs it.
- **Orphaned volumes are found from outside the cluster**, by a daily GitHub
  workflow with that read-only token in a repo secret, reporting to the
  existing infra Telegram bot and silent when clean. An orphan is a volume
  attached to no server in two checks 15 minutes apart, so a volume moving
  between nodes is not reported. The kube API is VPN-only, so the workflow
  sees only Hetzner; that is the point. After a total loss the detached
  PostgreSQL volumes are the newest copy of the data, newer than R2 by up to
  the RPO, and a detector inside the cluster would have died with it — so the
  report says the volume may hold data before it gives the delete command.
  Workflow logs are public; volume ids are already published in the runbooks.
- **The Restore Drill is a monthly in-cluster CronJob in its own namespace,
  `pg-drill`**, whose RBAC lets it create and delete CloudNativePG `Cluster`
  objects there and nowhere else. It restores one instance from the
  `ObjectStore` to the **end of the WAL** — the newest base backup and the
  whole chain after it, the same path a real recovery takes — asserts, and
  deletes the cluster. It reads with a **second R2 token, Object Read only**,
  in SOPS, and has no WAL archiver, so a fault in the drill cannot write a
  second timeline into the locked bucket. Its volume uses `hcloud-volumes`
  (Delete), never the Retain class, or every drill would leave an orphan.
- **The drill asserts WAL completeness and data presence, not row counts.**
  The body's "asserts row counts" never said compared to what: against live
  production the drill would need a production database credential and a
  tolerance, and against a fixed floor expiring rows fail it for nothing. It
  asserts instead: (1) the restored cluster is `Ready` within 30 minutes;
  (2) `pg_last_wal_replay_lsn()` is at or past production's last archived WAL,
  read from the production `Cluster` status before the restore — physical
  replay is exact, so reaching that LSN means the rows are production's rows;
  (3) every database production declares (`initdb.database` and the
  `Database` objects) exists, with at least one user table and more than zero
  rows. The drill reads production metadata only, never production data.
- **Failures are loud by two paths.** The drill reports to the infra Telegram
  bot on success and on failure; the success message carries the LSN reached
  and the databases checked. Backup freshness is watched from outside: the
  daily workflow that reports Orphaned Volumes also lists `tosak-pg-backups`
  with the read-only R2 token and reports when the newest base backup is
  older than 26 hours. That one check catches a stopped plugin, a dead token
  and a dead cluster alike, because nothing inside a dead cluster can report
  its own death.
- **The drill leaves a trace outside the cluster.** It writes its result
  (`drill/<yyyy-mm>.json`: time, LSN reached, databases, pass or fail) to a
  separate bucket, `tosak-drill-results`, with a token that can write to that
  bucket only. The daily workflow reports when the newest pass is older than
  35 days, so a drill that silently stops running is found. Rejected: a
  `repository_dispatch` from the drill (the PAT would need `Contents: write`
  on a repository whose admin bypasses branch protection), and running the
  drill on GitHub's runners (production user data decrypted on third-party
  machines to *test* a backup).
- **Known gap, owned by the monitoring scope:** the outside watcher is a
  scheduled GitHub workflow, and GitHub disables those in a public repository
  after 60 days without repository activity — silently. A heartbeat service
  that notices missing pings would close it, and would also cover the drill,
  the backups and the certificate renewal. That is decided with monitoring,
  not here.
- **`serverName` is explicit and carries a generation: `tosak-pg-cluster-g1`.**
  A real recovery reads generation N and archives to N+1, and
  `pg-cluster.yaml` records the current one. CloudNativePG refuses to archive
  into a non-empty path; a recovered cluster must keep the name
  `tosak-pg-cluster` because every application addresses
  `tosak-pg-cluster-rw`; and the 30-day lock means the old path cannot be
  cleared. With the default `serverName`, the first day after a disaster
  would also be a day without backups. A date was rejected because two
  recovery attempts in one month would collide. The drill never archives, so
  a passing drill does not prove this step — the recovery runbook must.
- **Credentials this design adds**, every one created by the operator:

  | Credential | Can | Lives in |
  |---|---|---|
  | R2 token, Object Read & Write, `tosak-pg-backups` | archive WAL and base backups | SOPS (`pg-cluster`), password manager |
  | R2 token, Object Read, both buckets | restore; read freshness and drill results | SOPS (`pg-drill`), GitHub repo secret |
  | R2 token, Object Read & Write, `tosak-drill-results` | write a drill result | SOPS (`pg-drill`) |
  | Hetzner token, Read | list volumes | GitHub repo secret |
  | Infra Telegram bot token (a copy) | send a message as that bot | SOPS (`pg-drill`) |

  No credential that runs on a schedule can delete a backup or change a
  bucket's configuration.
