---
title: "kluster Worker commands built and proven in the rehearsal project"
description: "kluster gained node add, remove, replace, list and reset, and every apply path ran in the rehearsal project; live received read-only checks only."
type: log
date: 2026-10-06
topics: [provisioning, kubernetes]
---

This task added the Worker commands to `tools/kluster` (Phase 6 of the [rebuild Plan](../runbook/rebuild-2026-09-20.md)) on the branch `feat/kluster-worker-commands`, and proved each `--apply` path on the rehearsal Control Plane. The operator-facing steps are in [the Workers runbook](../runbook/workers.md) and [the host keys runbook](../runbook/host-keys.md). Live saw only read-only checks.

## What was built

The Worker set became one file per environment: live keeps `infra/workers/terraform.tfvars`, the rehearsal has the committed `infra/workers/rehearsal.tfvars` (`workers = {}`), and the config refuses a rehearsal file equal to the live one. A new `workerset` package edits only the one entry's tokens with hclwrite, keeps comments, re-parses, and requires exactly the intended set. It also picks the next free address of each pool. Opening the workers Module now runs a commit gate first: the set file must be tracked and clean.

Next came the Control Plane access. `kluster pin import <vpn-address>` copies the one ed25519 key that `~/.ssh/known_hosts` holds for a hand-built server into kluster's pins; Plan Mode shows the key and its SHA256, `--apply` writes and reads it back. Remote commands can run through `sudo -n`, which the live Control Plane login (`cp-dev` over WireGuard) needs. `kluster node list` compares the set, the Hetzner servers and the Nodes, read-only.

The Worker Stages followed: WireGuard spoke, hub peer, join with a 15-minute token, and a Node-ready poll. Each hides the hub key and the token from terminal output. Then the commands themselves:

- `node add` runs twice with a commit between: the first run writes the set entry, the second builds the server, rotates its host key, joins it and waits for Ready.
- `node remove` runs twice the same way: the first drains, deletes the Node, removes the hub peer and drops the entry; the second deletes the server.
- `node replace` runs once: a database gate, the retire Stages, a Terraform `-replace` of that one server, and a full build.
- `reset` replaces every declared Worker in name order, waiting up to 15 minutes for the database gate between Workers, and says what is left if it stops.

## What the rehearsal proved

`node add` for `k8swk1` and `k8swk2` ran both runs under `--apply`: each Node reached Ready, bootstrap SSH closed and read back closed, and `node list` reported no drift. `node replace k8swk1` and `reset` ran under `--apply`; every replaced Worker was Ready before the next. `node remove` ran both runs for both Workers, and `down` left the rehearsal project empty (read back). The log of the run is the commits `f1fda81` to `98a7cfd` on the branch.

One assumption became proof: with `ignore_changes = [user_data]`, a `-replace` gives the new server the new seeded key. The first root login verified against the new pin three times.

## Live checks

With the operator's yes, `kluster pin import 10.100.0.1 --apply` pinned the live Control Plane (`SHA256:58iiV54MDXGf5wKb8m1uP8tqwltJoyhE3gBQ8SEuwjM`, read back with `ssh-keygen -lf`). Afterwards `kluster --env live node list` logged in as `cp-dev` and showed k8swk1 to k8swk3 Ready on v1.37.0 with no drift, and `terraform plan` in `infra/workers` said "No changes". No live `--apply` of add, remove or replace has run.

## Faults found on the way

- Appending to a token slice overwrote the closing brace of the `workers` map. A unit test failed with "Missing expression"; a clone fixed it.
- x/crypto/ssh v0.55 offers ECDSA and RSA host keys before ed25519, so a pin of only the ed25519 key would have been refused by the hand-built live Control Plane. `remote.DialVia` now asks for ed25519 only. Proven from the source; the live dial was not run before the fix (assumed it would have failed).
- Opening the Worker firewall alone planned the Control Plane rule at its default, which would close it. `node add` now opens both in one plan (`TestOpenBothInOnePlan`).
- hcloud provider 1.69.0 left port 22 on the Worker firewall after each close apply; kluster's API fallback cleared it and read it back.
- The rehearsal never touched the operator's laptop files: its laptop Stages write under `~/.config/kluster/rehearsal/`.
- `infra/workers/.terraform` held a provider symlink into a dead scratchpad, so a plan failed until the link was removed and `init` re-run. kluster's own runs use `.terraform-rehearsal` and were unaffected.
- hclwrite writes an emptied set as `workers = {` and `}` on two lines; the committed form was restored.

## Open items

- `versions.kubernetes: "v1.37"` pinned the minor only, and the rehearsal built v1.37.1 Nodes while live runs v1.37.0. After the PR opened, the operator chose to pin the live Control Plane's exact patch: `versions.kubernetes: "v1.37.0"`, validated as a full release, with the packages installed as `1.37.0-*` and probed for that exact patch (`3c49dad`). The checks passed read-only on the live Control Plane and a simulated `apt-get -s install` there resolved the glob to `1.37.0-1.1`; no Worker has been built with the pin yet.
- The database gate's CNPG branch and a drain that holds a CNPG primary are unit-tested only (assumed to work: the primary PDB holds the drain and CNPG switches over); task 5 (`kluster up`) proves them.
- The opt-in `KLUSTER_TF_TEST=1` test did not run; its fixture likely fails config validation (assumed).
- `node list --env rehearsal` on an empty project says bootstrap SSH is closed, which misleads (assumed cosmetic).
- A kubelet re-registers a deleted Node when it restarts, so a Worker rebooted between the two `node remove` runs brings its Node back and the second run refuses (assumed from kubelet behaviour, not tested).

## Lesson candidates

None.
