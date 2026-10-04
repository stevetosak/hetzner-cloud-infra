---
title: "The rehearsal project: set up, run, clear away"
description: "What the operator creates once so kluster can rehearse in a second Hetzner project, how a rehearsal run goes, and how to prove the project is empty again afterwards."
type: procedure
topics: [provisioning, security]
---

Nothing kluster does reaches the live cluster before it has run in a second
Hetzner project ([ADR 0009](../adr/0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md)). A Hetzner project is its own namespace, so the
rehearsal uses the same resource names and the same Terraform Modules as live;
what differs is every credential and the bucket that holds the state.

kluster keeps the two apart in three ways. `--env rehearsal` reads its own
environment variables, and the terraform child process gets a clean
environment with no `TF_*`, `AWS_*` or `HCLOUD_*` from the shell, so the live
token cannot reach it. Each Module keeps a separate data directory,
`.terraform-rehearsal`, so a rehearsal init never repoints the live
`.terraform`. And before it does anything, a rehearsal run reads the project
over the API and stops if it holds `46.62.209.249`, the live Control Plane's
address.

## Set up once (operator)

Every value below is created by the operator and never pasted into a
transcript.

1. In the Hetzner console, create a project named `tosak-rehearsal`. In it,
   create two API tokens: one **Read & Write** for kluster, one **Read** for
   the watcher.
2. In Cloudflare R2, create the bucket `hetzner-cloud-infra-rehearsal` and an
   R2 API token with Object Read & Write **on that bucket only**. Rehearsal
   state never shares the live bucket or its token.
3. Keep the three kluster values in the password manager and in a file
   outside the repository, readable by the operator only, for example
   `~/.config/kluster/rehearsal.env` (`chmod 600`):

   ```sh
   export KLUSTER_REHEARSAL_HCLOUD_TOKEN=…
   export KLUSTER_REHEARSAL_R2_ACCESS_KEY_ID=…
   export KLUSTER_REHEARSAL_R2_SECRET_ACCESS_KEY=…
   ```

4. Add the Read token as the repository secret
   `HCLOUD_REHEARSAL_READONLY_TOKEN`. Until it exists, the daily
   [recoverability watch](../../.github/workflows/recoverability-watch.yml) reports its rehearsal check as broken.

## Run a rehearsal

From `tools/kluster`, with `infra/.envrc` and the rehearsal file both
sourced (kluster reads the live variables only to refuse a rehearsal value
that equals one of them):

```sh
go build -o kluster .
./kluster --env rehearsal rehearse core          # Plan Mode: changes nothing
./kluster --env rehearsal rehearse core --apply
```

`rehearse core` proves the safety core: it builds `shared` from empty, opens
bootstrap SSH on the Worker firewall, creates the Workers with seeded host
keys, logs in to each and rotates its key ([host keys](./host-keys.md)), and closes SSH with the
API readback of the [workers runbook, step 9](./workers.md#9-close-bootstrap-ssh). The close runs at the end of
every run, a failed or interrupted one included.

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

## How to know it worked

`down --apply` lists the project again after deleting and fails unless it is
empty; it prints `read back: the rehearsal project is empty`. The watcher
checks the same thing from outside every morning: the project must be empty,
or its oldest billable resource younger than six hours, or a Telegram alert
goes out. A rehearsal left running costs the monthly caps, about €32 a month,
and Hetzner has no spending cap (assumed: none was found).
