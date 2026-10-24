---
title: "The rehearsal project: set up, run, clear away"
description: "What the operator creates once so kluster can rehearse in a second Hetzner project, how a rehearsal run goes, and how to prove the project is empty again afterwards."
type: procedure
verified: 2026-10-24
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

4. Add the Read token as the repository secret
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
In a rehearsal the `zone` Stage refuses by design: the load balancer's address
is not the one the live Cloudflare zone holds, and the refusal is the proof that
the mismatch report works. Rehearsal Secrets are made fresh in memory, and a
Secret marked `rehearsal: omit` in `secrets.yaml` is not made at all.

The rehearsal Control Plane has the live VPN addresses, so its WireGuard and
kubeconfig edits go to stand-in files under `~/.config/kluster/rehearsal/`,
and kluster proves the route with its own in-process peer
([control-plane runbook](./control-plane.md#how-kluster-carries-this-out)). To reach the rehearsal cluster by hand while it runs, use
`ssh root@<its public address>` during the run or reopen port 22 with
`shared`; the operator's `wg0` keeps pointing at the live hub.

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
everything they were attached to, and empties the rehearsal pins file. It is
refused for `--env live`.

🔴 `down` can race the load balancer's Primary IPs: Hetzner refuses to delete a
Primary IP that is still assigned (`must_be_unassigned`) for a short time after
the load balancer is gone. A second `down --apply` then empties the project
(seen 2026-10-24; a Plan item makes `down` wait for the unassign).

## How to know it worked

`down --apply` lists the project again after deleting and fails unless it is
empty; it prints `read back: the rehearsal project is empty`. The watcher
checks the same thing from outside every morning: the project must be empty,
or its oldest billable resource younger than six hours, or a Telegram alert
goes out. A rehearsal left running costs the monthly caps, about €32 a month,
and Hetzner has no spending cap (assumed: none was found).
