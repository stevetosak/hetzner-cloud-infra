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
