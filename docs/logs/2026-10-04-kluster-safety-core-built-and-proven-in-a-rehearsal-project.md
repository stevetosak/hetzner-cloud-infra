---
title: "kluster safety core built and proven in a rehearsal project"
description: "The kluster tool was rebuilt with one saved-plan path, pinned host keys and a read-back SSH close, then proven in a separate Hetzner project."
type: log
date: 2026-10-04
topics: [provisioning, security]
---

This chunk rebuilt `tools/kluster` from scratch as the safety core of Phase 6 (see [ADR 0009](../adr/0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md)), not from the open PR #10. It ended with a full rehearsal run in a separate Hetzner project. The visible result is six commits on `feat/kluster-safety-core`; no live resource was touched.

## Decisions taken first

The operator deleted the stale ignored binary `tools/kluster/kluster` in the main checkout and ran `chmod 600` on `infra/.envrc`, which was 664. The operator then approved the library set: hcloud-go v2.50.0, terraform-exec v0.25.3 with terraform-json v0.28.0, cobra v1.10.2, x/crypto/ssh with knownhosts, yaml.v3, go-cmp and x/sync errgroup. The Intent Assertion and the Stage runner stayed hand-rolled, because they are domain logic.

Three design choices went into the ADR 0009 Amendment of 2026-10-04: the rehearsal Terraform state lives in a separate R2 bucket with a bucket-scoped token, not a prefix in the live bucket; the seeded host key is rotated at the first login; and the chunk-1 rehearsal is shared from empty, SSH open and close with read-back, test Workers with seeded and rotated keys, `down`, and the watcher check.

## What was built

Both Terraform Modules, `infra/workers` and `infra/control-plane`, gained a sensitive `user_data` variable and `lifecycle.ignore_changes = [user_data]`. The Go packages are `config`, `env`, `intent`, `tf`, `cloud`, `sshgate`, `hostkey`, `remote`, `stage`, `stages` and `provision`. Each enforces one rule:

- `config` refuses a rehearsal environment that shares a variable name or bucket with live.
- `env` builds a clean Terraform child environment and refuses a rehearsal value equal to the live value.
- `intent` matches resources with a star-only glob, because `path.Match` reads `[` as a character class and broke on `hcloud_server.workers["k8swk4"]`.
- `tf` keeps the variable file and the saved plan in a 0700 directory under `$XDG_RUNTIME_DIR`, removed at exit, and applies only the saved plan.
- `cloud` refuses any project that holds the live Control Plane address, and `down` deletes in order after lifting protection.
- `sshgate` opens and closes port 22 and reads the result back over the API.
- `hostkey` and `remote` pin keys per environment and never learn a key on first use; a host key mismatch is never retried.

The watcher gained a rehearsal-age check in `.github/scripts/watch.mjs`, with a workflow step using the secret `HCLOUD_REHEARSAL_READONLY_TOKEN`; a missing secret counts as a broken check, not a skip. The Procedures `docs/runbook/host-keys.md` and `docs/runbook/rehearsal.md` were added, both verified 2026-10-04.

## What was proven

- `go test -race ./...` passes in all 11 packages. The opt-in `KLUSTER_TF_TEST=1` test against the real terraform 1.16.0 binary passed on a throwaway Module with a local backend: plan mode changes nothing, a mismatch under `--apply` aborts, a matching apply applies, and a replace is refused. The watcher tests pass, 22 of 22.
- With the operator's approval, `terraform plan -detailed-exitcode` in `infra/workers` and `infra/control-plane` returned `No changes`, exit 0, plain and with `-var user_data=...` set for k8swk1-3 and the Control Plane. The plain plan alone proved nothing: a null value against state gives no diff even without `ignore_changes`, and the provider's `userDataDiffSuppress` (v1.69.0 source) does not hide a changed value. Only the plan with a value set is the proof.
- Provider 1.69.0 stores `user_data` as a SHA1 hash. This was read in the released source (`internal/server/resource.go`, `userDataHashSum`) and then proven in the rehearsal: `terraform state pull` of the workers showed a `user_data` length of 28 and no key text for all three servers.
- `rehearse core --apply` in `tosak-rehearsal` created 9 shared resources, opened SSH (an update, read back open), created Workers k8swk1-3 (ids 168624929-31) with seeded keys, and pinned them. The first login was verified against the seeded key, and `rotate-host-key` finished on all three servers. Exit code 0.
- A check from outside after the run found one pin per address, an empty worker firewall rule list applied to 3 servers, a Control Plane firewall with only UDP 51820, and TCP 22 closed or filtered on all three addresses.

## What went wrong

- 🔴 The close `apply` reported success, but the API read-back still showed `tosak-worker-firewall` admitting port 22. This is the third time the provider 1.69.0 defect (a firewall cannot go from one rule to none) was hit. The `set_rules` fallback cleared it, the read-back showed it closed, and `plan shared` showed no changes. The read-back is why the tool caught it.
- 🔴 `down` in plan mode listed the primary IPs that servers create on their own. Deleting the servers deletes those IPs too, so `DeleteAll` would have failed with `not_found` on a stale list. The fix, made before any apply, treats `not_found` as already gone, with a unit test. The real `down --apply` logged "already gone" three times, deleted the rest including the protected `tosak-cp-ip`, and the project read back empty (commit `776046d`).
- The first rehearsal `init` returned 403 Forbidden on HeadObject. The config named `hetzner-cloud-infra-rehearsal`, while the operator's bucket and token scope were `hetzner-cloud-infra-staging`. Config and docs now use the real name (commit `0da4e72`). A hand-made curl SigV4 probe was unreliable, because a control against the live bucket with working keys gave `SignatureDoesNotMatch`, so it was not used as evidence.
- The Hetzner token was first pasted with 63 characters and the API answered `unauthorized`; the second paste had 64.
- A read-only `terraform fmt -check` ran on the two Modules without an explicit request. It was reported to the operator.

After `down`, the rehearsal state still lists the deleted resources, but `rehearse core` in plan mode plans the same 9 creates without error, because the refresh drops the 404s. The project listed empty again.

## Not proven

- Assumed: pods can reach the metadata service at 169.254.169.254, which is the reason for the host key rotation.
- The watcher rehearsal step has not run in GitHub Actions. It runs only after a push, or with `workflow_dispatch` on the branch.
- The sub-item "Close PR #10 with a link" is open, because it needs the new PR and the push waits for approval.

## Next

The operator approves the push of `feat/kluster-safety-core`, the code PR opens, and PR #10 closes with a link to it. Optionally, `gh workflow run recoverability-watch.yml --ref feat/kluster-safety-core` proves the new secret. Phase 6 chunk 2 is `kluster cp init` into an empty slot, proven by a rehearsal build from an empty project.
