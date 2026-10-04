---
title: "The cluster is reconstructible from this repo plus one bucket"
description: "What must survive a total loss, what is rebuilt from git, how the databases are backed up to R2 and proven by a monthly restore drill."
type: decision
status: accepted
date: 2026-09-20
topics: [backups, secrets, gitops, databases]
---

## Status

Accepted, and **amended** on 2026-09-22 (SOPS), 2026-10-01 (the backup
design) and **2026-10-02, which reverses one 2026-10-01 decision**: the bucket
lock covers the WAL only. Read **Amendments** at the end before building
anything from the body.

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
  [ADR 0001](./0001-cluster-network-plan.md).

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
- **A 30-day bucket lock rule, no prefix, from the first backup.**
  *Superseded 2026-10-02: the rule covers `tosak-pg-cluster-g1/wals/` only —
  a no-prefix lock fails every base backup. See below.* R2's Object
  Read & Write permission includes delete, and that token lives in the
  cluster, so without a lock the cluster could erase its own history. The
  in-cluster token cannot edit bucket configuration; only the operator can
  remove the rule, from the dashboard. The lock does not fight retention: the
  plugin's 30-day recovery window deletes only objects older than the newest
  backup before now − 30 days, which are normally outside the lock (a segment
  uploaded late, after archiving was broken for a while, can still be locked;
  its delete then fails and the next retention run retries it). Cost: no
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
  read from the production `Cluster` status before the restore (*2026-10-02:
  that status does not carry it under plugin archiving; the drill lists the
  bucket instead, and reads the end of its replay from the timeline history,
  because the function is NULL after CloudNativePG's recovery — see the last
  amendment*) — physical
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
  its own death. (*2026-10-02: it does NOT catch a broken WAL archiver — a
  base backup from a standby does not wait for archiving. See below.*)
- **The drill leaves a trace outside the cluster.** It writes its result
  (`drill/<yyyy-mm>.json` — *one object per run since 2026-10-02, see the
  last amendment*: time, LSN reached, databases, pass or fail) to a
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

  No credential that runs on a schedule can change a bucket's configuration.
  *2026-10-02: the backup token CAN delete base backups — they are not locked
  (see below) — and the plugin's retention deletes expired ones with it on
  purpose. Only the WAL is beyond its reach.*

**2026-10-02 — the bucket lock covers the WAL only; found while building it.**
The 2026-10-01 lock rule (30 days, no prefix) would have failed every base
backup. What was found, and what replaces it:

- **barman 3.19.0 and later writes `backup.info` twice** in one base backup:
  status `STARTED` when the copy begins, then the same key again with `DONE`
  or `FAILED` when it ends (`CloudBackup.coordinate_backup` in
  `barman/cloud.py`, read at `release/3.20.1`, the version plugin v0.15.1
  ships). **An R2 bucket lock refuses overwrites as well as deletes**, and R2
  has no object versioning — on AWS S3 the same overwrite succeeds by making a
  new version. Measured on this bucket with the backup token under the
  no-prefix rule: the second `PutObject` and a `DeleteObject` both returned
  `ObjectLockedByBucketPolicy`, and the object kept its first content. With
  that rule, every base backup would end `FAILED` and leave a `STARTED`
  `backup.info` that nobody could delete for 30 days. Upstream: barman issue
  #1195, open, treated as a feature request.
- **Decision: one lock rule, prefix `tosak-pg-cluster-g1/wals/`, 30 days.**
  barman writes every WAL segment once, under a name that never repeats, so
  the lock costs the WAL nothing. Rejected: pinning plugin v0.12.0, the last
  release on barman 3.18 (one write) — it predates CloudNativePG 1.30, and
  every later upgrade would break base backups again; and no lock at all.
- **Cost: base backups are not locked.** A compromised cluster can delete
  them with its own token, and WAL without any base backup restores nothing.
  The daily workflow reports a missing or stale base backup within 26 hours;
  that is detection, not prevention. The lock still stops the cluster, or a
  barman fault, from erasing the WAL history.
- **Each generation needs its own rule.** A prefix is literal, so a recovery
  that moves archiving to `tosak-pg-cluster-g2` must add a rule for
  `tosak-pg-cluster-g2/wals/` before the recovered cluster archives. The
  recovery runbook carries this step.
- **Revisit when barman stops overwriting `backup.info`** (issue #1195): then
  the rule goes back to no prefix and base backups are locked too.
- Three build-time facts, as promised in the decision above: `zstd` is
  accepted for WAL (base backups offer no `zstd`; they use `gzip`). A base
  backup taken from a standby **does not wait for WAL archiving** —
  `pg_backup_stop()` waits only on a primary, or on a standby with
  `archive_mode = always`, and CloudNativePG sets `on` — so a broken archiver
  does not fail the base backup or trip the 26-hour check; monitoring must
  watch archiving itself. And **`Cluster.status` does not record the last
  archived WAL** under plugin archiving, nor does `ObjectStore.status`, so the
  drill's assertion (2) needs another source for production's last archived
  WAL; that is decided with the drill. Measurements:
  [`docs/runbook/cluster-services.md`](../runbook/cluster-services.md), step 15.

**2026-10-02 — the Restore Drill, decided while building it.** Three inputs
the 2026-10-01 design left open or got wrong, chosen by the operator, and one
change found in the build:

- **Production's last archived WAL is read from the bucket**, not from the
  production `Cluster` status, which does not carry it. Before the restore,
  the drill lists `<serverName>/wals/` with its read-only token and takes the
  newest segment name (skipping `.history`, `.partial` and `.backup`
  objects). The bucket is the thing being tested, so it is the right source,
  and no production credential is needed.
- **The end of the replay is read from the restored instance's timeline
  history**, not from `pg_last_wal_replay_lsn()`. CloudNativePG recovers in a
  separate Job that promotes and stops; the instance then starts normally,
  and on a normal start that function returns NULL. Promotion records the
  switch point of the new timeline in its `.history` file — exactly where the
  replay ended. Assertion (2) passes when that LSN lies in or past the newest
  segment listed.
- **The databases production declares are read from production metadata**:
  `initdb.database` from the production `Cluster`, and every `Database` object
  for it. A read-only Role in `pg-cluster` allows `get` on that one Cluster
  and `list` on `Database` objects — names and owners, no secrets. Rejected: a
  fixed list in the drill (drifts when a project is added), and the restored
  catalog itself (circular: a missing database cannot be found by asking the
  restore under test).
- **The drill reaches its own restored instance by `pods/exec`** and runs
  `psql` as `postgres` over the local socket, so no database credential
  exists for it at all. The grant is in `pg-drill` only.
- **One result object per run, `drill/<UTC timestamp>.json`**, not one per
  month: with `drill/<yyyy-mm>.json` a failed second run in a month overwrites
  that month's pass, and the watcher would report a drill that passed. The
  watcher reads every object under `drill/`, so it needed no change.
- The drill is a bash script on a pinned `alpine/k8s` image (kubectl, aws,
  jq, yq and curl in one image), in a ConfigMap generated by kustomize from
  `core/pg-drill/drill/`. Build and proof: [`docs/runbook/cluster-services.md`](../runbook/cluster-services.md),
  step 17.

**2026-10-04 — the password manager is read at run time, and secrets have
three kinds.** [ADR 0009](./0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md) makes kluster build the whole cluster unattended, which
needs every secret to have a path a program can follow. SOPS in git stays the
store; nothing above changes. What was decided:

- **Three kinds, three paths** (named in `CONTEXT.md`). A *Generated Secret*
  — a random password — is made by kluster when it is missing and piped
  straight into its `*.enc.yaml`, never into a plaintext file or the output.
  An *Issued Secret* — a Hetzner, Cloudflare, R2 or Telegram token — can only
  come from its provider's console, so the operator stores it once. A
  *Derived Credential* — certificates, a join token, the administrator's
  kubeconfig — is made fresh by each build and never stored.
- **The password manager is the root, and only the root.** It holds the age
  key and the few Issued Secrets needed before any cluster exists: the Hetzner
  token and the R2 keys for the Terraform state. kluster reads them through
  the Bitwarden CLI (`bw`, unlocked once per shell), keeps them in memory and
  passes them to Terraform and SOPS as the child process's environment. The
  recovery path above is unchanged; it is now followed by a program instead
  of by hand.
- **`infra/.envrc` retires as a plaintext file.** It held five values in
  plaintext, two of them unused WireGuard keys (`grep` over
  `infra/`, `tools/` and `docs/` on 2026-10-04 found no reader). It becomes
  `bw get` calls, or goes.
- Bitwarden Secrets Manager with the External Secrets Operator was weighed and
  lost: the cluster would depend on an outside service to start, and ESO
  needs a machine token living in the cluster.
