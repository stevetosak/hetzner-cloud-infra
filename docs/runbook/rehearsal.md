---
title: "The rehearsal project: set up, run, clear away"
description: "What the operator creates once so kluster can rehearse in a second Hetzner project, how a rehearsal run goes, and how to prove the project is empty again afterwards."
type: procedure
verified: 2026-10-25
topics: [provisioning, security]
---

Nothing kluster does reaches the live cluster before it has run in a second
Hetzner project ([ADR 0009](../adr/0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md)). A Hetzner project is its own namespace, so the
rehearsal uses the same resource names and the same Terraform Modules as live;
what differs is every credential and the bucket that holds the state.

kluster keeps the two apart in three ways. `--env rehearsal` reads its
credentials from its own Bitwarden item, `kluster/rehearsal`, and the
terraform child process gets a clean
environment with no `TF_*`, `AWS_*` or `HCLOUD_*` from the shell, so the live
token cannot reach it. Each Module keeps a separate data directory,
`.terraform-rehearsal`, so a rehearsal init never repoints the live
`.terraform`. And before it does anything, a rehearsal run reads the project
over the API and stops if it holds `46.62.209.249`, the live Control Plane's
address.

🔴 **A separate project does not separate the account's quotas.** Hetzner counts
Primary IPs per account, so a rehearsal draws from the pool live uses. The
rehearsal load balancer holds two of them, one IPv4 and one IPv6. On 2026-10-24
the load balancer was refused with `Primary IP limit exceeded
(resource_limit_exceeded)` while the account held 9 of 10; Hetzner raised the
limit to 15 the same day. Read the count in the console before a rehearsal that
creates a load balancer ([Lesson](../lessons/an-isolation-boundary-is-checked-for-the-quotas-it-still-shares-before-a-design-relies-on-it.md)).

## Set up once (operator)

Every value below is created by the operator and never pasted into a
transcript.

1. In the Hetzner console, create a project named `tosak-rehearsal`. In it,
   create two API tokens: one **Read & Write** for kluster, one **Read** for
   the watcher.
2. In Cloudflare R2, create the bucket `hetzner-cloud-infra-staging` and an
   R2 API token with Object Read & Write **on that bucket only**. Rehearsal
   state never shares the live bucket or its token.
3. Keep the values in the Bitwarden item `kluster/rehearsal`, as hidden
   fields: `hcloud_token` (the Read & Write token), `r2_access_key_id` and
   `r2_secret_access_key`, plus `hcloud_cluster_token`, a token of its own for
   the cluster's cloud controller. kluster reads the item through `rbw`; no
   file and no shell variable holds the values. The earlier
   `~/.config/kluster/rehearsal.env` file is no longer read, and the operator
   deletes it.

4. For the database ([recovery runbook](./pg-recovery.md)): create the R2
   bucket `tosak-pg-backups-rehearsal`, and two more R2 tokens in Bitwarden.
   `kluster/r2-backup-rehearsal-rw` has Object Read & Write on that bucket
   only. `kluster/r2-backup-rehearsal-ro` has Object Read on the live bucket
   `tosak-pg-backups`: the rehearsal reads the live backups through it and
   holds no live token. The Secret Catalog names both as `rehearsalItem:`, and
   `kluster services check` refuses a `rehearsalItem` equal to `item`.

5. Add the Read token as the repository secret
   `HCLOUD_REHEARSAL_READONLY_TOKEN`. Until it exists, the daily
   [recoverability watch](../../.github/workflows/recoverability-watch.yml) reports its rehearsal check as broken.

## Run a rehearsal

From `tools/kluster`, with the Bitwarden vault unlocked (nothing is sourced;
kluster reads `kluster/live` only to refuse a rehearsal value that equals a
live one):

```sh
go build -o kluster .
./kluster --env rehearsal rehearse core          # Plan Mode: changes nothing
./kluster --env rehearsal rehearse core --apply
```

A Control Plane build rehearses the same way, after `shared`:

```sh
./kluster --env rehearsal shared --apply
./kluster --env rehearsal cp init                # Plan Mode
./kluster --env rehearsal cp init --apply
```

After the Control Plane and the Workers, the services seed installs `core/` in
the rehearsal. Push the branch the rehearsal reads first, because the rehearsal
Applications track the branch `rehearsal`, not `master`:

```sh
git push -f origin HEAD:rehearsal
./kluster --env rehearsal services seed          # Plan Mode: diffs per Application
./kluster --env rehearsal services seed --apply
```

The stages and what to read in each are in the [services seed runbook](./services-seed.md).
In a rehearsal the `zone` Stage proves that the live zone is untouched, and the
Stage `rehearsal-cuts` proves that no live link remains. Rehearsal Secrets are
made fresh in memory; a key with a `rehearsalItem:` is read from that
Bitwarden item, and a Secret marked `rehearsal: omit` in `secrets.yaml` is not
made at all.

The rehearsal Control Plane has the live VPN addresses, so its WireGuard and
kubeconfig edits go to stand-in files under `~/.config/kluster/rehearsal/`,
and kluster proves the route with its own in-process peer
([control-plane runbook](./control-plane.md#how-kluster-carries-this-out)).
The operator's `wg0` keeps pointing at the live hub.

## A whole rehearsal with kluster up

Since 2026-10-25 one command builds the rehearsal from an empty project, and
the single commands above are what it composes
([ADR 0009, Amendment 2026-10-25](../adr/0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md#amendments)).
`infra/workers/rehearsal.tfvars` declares the same three Workers as live and
stays committed between rehearsals; `up` builds what it declares and never
writes it, and `down` does not touch it.

```sh
git push -f origin HEAD:rehearsal                # the revision the rehearsal's ArgoCD reads
./kluster --env rehearsal up --db fresh          # Plan Mode: preflight, then every step as a new Stage
./kluster --env rehearsal up --db fresh --apply  # about 20 minutes
./kluster --env rehearsal up --db fresh --apply  # a rerun: every Stage done, nothing built
./kluster --env rehearsal down --apply
./kluster --env rehearsal up --db recover --apply
./kluster --env rehearsal down --apply
```

`up` first runs the up-preflight, which prints every failure at once. The tree
must be the commit that `origin/rehearsal` holds, with no tracked change in
`core/`, `projects/`, `infra/`, `secrets.yaml` or `tools/kluster/kluster.yaml`;
the Worker set must be committed and not empty; and no server may exist that
is not a Node of the current cluster, which `up` never replaces (delete it by
hand, or use `node replace`). The proof of 2026-10-25 ran the sequence above:
the first `up` finished every Stage, the reruns built nothing, and the
`--db recover` run restored `doma` with 33 tables, as live has (see the
[Log](../logs/2026-10-25-kluster-db-create-and-kluster-up-built-and-rehearsed-end-to-end-down-waits-for-the-primary-ip-unassign.md)).
A rehearsal never writes the database generation bump. The database Stages are
in the [recovery runbook](./pg-recovery.md#the-stages-of-db-create).

## Look at a running rehearsal

🔴 **Do not use the rehearsal kubeconfig while `wg0` holds live.**
`~/.config/kluster/rehearsal/kubeconfig` points at `https://10.100.0.1:6443`,
and the laptop's `wg0` is the live tunnel at that address. A `kubectl` with that
file reaches the live API server; TLS refused it (`x509: certificate signed by
unknown authority`) and nothing ran, but it must not be tried again. Proof:
`ip route get 10.100.0.1` gives `dev wg0`. kluster itself never uses `wg0` for a
rehearsal.

To look at a rehearsal, open bootstrap SSH, work as root on the Control Plane,
and close it again:

```sh
./kluster --env rehearsal ssh open --apply
# ssh root@<the rehearsal Control Plane>, pinned host key; kubectl and psql work there
./kluster --env rehearsal ssh close --apply      # reads the firewall back
```

`rehearse core` proves the safety core: it builds `shared` from empty, opens
bootstrap SSH on the Worker firewall, creates the Workers with seeded host
keys, logs in to each and rotates its key ([host keys](./host-keys.md)), and closes SSH with the
API readback of the [workers runbook, step 9](./workers.md#9-close-bootstrap-ssh). The close runs at the end of
every run, a failed or interrupted one included; SIGHUP (a closed terminal) is caught too, SIGPIPE is not.
Every `cp`, `node` and `reset` run also warns at start for each firewall that admits port 22.

## Clear it away

```sh
./kluster --env rehearsal down            # lists what it would delete
./kluster --env rehearsal down --apply
```

`down` deletes through the Hetzner API, not `terraform destroy`, because the
`prevent_destroy` guards in `shared` refuse a destroy by design. It lifts
delete and rebuild protection first, deletes servers and load balancers, then
everything they were attached to, and empties the rehearsal pins file. After the
project is empty it also deletes every object in the rehearsal bucket
`tosak-pg-backups-rehearsal` and reads the listing back empty; Plan Mode shows
the object count. It is refused for `--env live`.

Hetzner refuses to delete a Primary IP that is still assigned
(`must_be_unassigned`) for a short time after the load balancer is gone. `down`
now waits for the unassign, up to two minutes, and prints `primary IP … is still
assigned: waiting up to 2m0s for Hetzner to unassign it` when it starts to wait.
Before 2026-10-25 the delete failed and a second `down --apply` emptied the
project (seen 2026-10-24). The wait was proven in the second `down` of
2026-10-25: the line printed, then the IP went with the load balancer.

## How to know it worked

`down --apply` lists the project again after deleting and fails unless it is
empty; it prints `read back: the rehearsal project is empty`, and then reads
the rehearsal bucket back empty. The watcher
checks the same thing from outside every morning: the project must be empty,
or its oldest billable resource younger than six hours, or a Telegram alert
goes out. A rehearsal left running costs the monthly caps, about €32 a month,
and Hetzner has no spending cap (assumed: none was found).
