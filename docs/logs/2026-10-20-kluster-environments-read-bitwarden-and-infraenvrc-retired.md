---
title: "kluster environments read Bitwarden and infra/.envrc retired"
description: "Both environments now read their credentials from a Bitwarden item, kluster's environment-variable source is deleted, and infra/.envrc is gone; the live cluster now holds a Hetzner token of its own."
type: log
date: 2026-10-20
topics: [secrets, security, provisioning]
---

This task closed the Phase 6 item "live credentials from a Bitwarden item and `infra/.envrc` retired" in the [rebuild Plan](../runbook/rebuild-2026-09-20.md). Until now only the rehearsal read `kluster/rehearsal`; live still read three environment variables that `infra/.envrc` exported. The [2026-10-06 Amendment to ADR 0003](../adr/0003-recoverability.md) had set the rule: an environment-variable source stays during the build and is deleted in the same change once the rehearsal and a read-only live run pass. The rehearsal had passed on 2026-10-06; the live run was the missing proof.

## The change in kluster

`envs.live` in `tools/kluster/kluster.yaml` now names `bitwardenItem: kluster/live`, with the same four hidden fields as the rehearsal item: `hcloud_token`, `hcloud_cluster_token`, `r2_access_key_id` and `r2_secret_access_key`. The operator created the item on 2026-10-20. In the Go code the config fields `hcloudTokenEnv`, `r2AccessKeyIDEnv` and `r2SecretAccessKeyEnv` and the `getenv` fallback in `internal/env` were removed. `Resolve` is now `Resolve(ctx, cfg, name, vault)`, and a config whose environment has no item is refused with `envs.<name> must name a bitwardenItem`. The loader already rejects unknown keys, so a leftover `*Env` key in `kluster.yaml` fails to load without extra handling. The separation rules stayed: a rehearsal item must not hold the live bucket or the live values, and a missing live item still means there is nothing to collide with.

The env, cmd and tf tests needed a fake vault, and the one that existed was private to the env tests. It moved to a small shared package, `internal/bitwarden/fakevault`, so the three suites build on the same fake. Removing the `getenv` argument broke callers in `cmd/cp_test.go`, `cmd/exec_test.go` and `internal/tf/tf_test.go`, which `go vet ./...` found. `gofmt -l`, `go vet ./...` and `go test ./...` pass. The Terraform-backed tests in `internal/tf` are gated by `KLUSTER_TF_TEST=1` and were skipped, so those changes are proven by the vet and the compile only.

`kluster exec` is now the only way to hand-run Terraform, `infra/shared/import.sh` and the watcher's delete command. `.github/scripts/watch.mjs` prints `( cd tools/kluster && go run . exec -- sh -c 'curl … $TF_VAR_HCLOUD_TOKEN …' )`, and the `variables.tf` descriptions and `import.sh` comments no longer name the file. The runbook pages and `infra/README.md` were changed the same way.

## What was proven before the file was deleted

The read-only live run was done on 2026-10-20 in the operator's terminal, on the branch. `go run . exec -- sh -c 'printf "%s" "$TF_VAR_HCLOUD_TOKEN" | wc -c'` printed 64, and `go run . node list` in Plan Mode showed the three Workers running and Ready at v1.37.0 with "no drift". That shows the Hetzner token works; it says nothing about the R2 keys, because `node list` does not use them.

The R2 values were therefore proven by a hash. The `sha256sum` of the three values (Hetzner token, R2 key id, R2 secret) as `kluster exec` provides them equals the `sha256sum` of the same three taken from sourcing `infra/.envrc`. The two hashes matched, so the item holds the same values as the old file and none was lost. No value was printed and the hash is not copied here. After that proof the file was deleted. The WireGuard variables in it (`TF_VAR_WG_REMOTE_*`) had no reader, as ADR 0003 recorded on 2026-10-04, and went with it.

## What is still open

The live cluster held the shared Hetzner token until the same day. `kluster/live` has a `hcloud_cluster_token` (the operator added it on 2026-10-20), and `kluster cp init` reads it for `kube-system/hcloud`, but the live Secret had been made earlier by runbook step 8a from `$TF_VAR_HCLOUD_TOKEN` (assumed, not read). On 2026-10-20 the operator patched the live Secret's `token` key with `hcloud_cluster_token` and restarted `hcloud-cloud-controller-manager` and `hcloud-csi-controller`, the only two readers (found by their `secretKeyRef`). Proof: both pods Running within 23 s, no `unauthorized`, `401`, `forbidden` or `error` line in the last 200 log lines of either, all four nodes kept their `providerID`, and the SHA-256 of the live Secret's token differs from the SHA-256 of `TF_VAR_HCLOUD_TOKEN` from `kluster exec`. The cluster therefore holds a token of its own, and the Terraform token can be revoked without breaking the cloud controller or the CSI driver. The old shared token stays valid until the operator revokes it; that is not done.

The operator also owns `~/.config/kluster/rehearsal.env`. kluster no longer reads it, and the operator deletes it after a hash check against `kluster/rehearsal`. The next items on the Plan stay open: the `kluster up --db fresh|recover` rehearsal, `services seed`, and the guided pass over the risky kluster parts.
