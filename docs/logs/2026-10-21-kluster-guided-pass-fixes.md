---
title: "kluster guided pass fixes"
description: "Seven code fixes close the gaps the guided pass found in node, host key, SSH and kubeconfig handling."
type: log
date: 2026-10-21
---

The [guided pass, parts 3 and 4](./2026-10-21-kluster-guided-pass-parts-3-and-4.md) listed gaps in `tools/kluster`. Seven of them are fixed on branch `fix/kluster-guided-pass-gaps`, one commit each.

1. **`node remove` runs the same gates as `replace`.** The database gate (every CloudNativePG Cluster healthy) and the last-Worker check now run before the drain. Both commands share one path, `retire`, and `checkReplaceable` is renamed `checkRetirable`. In Plan Mode with no Control Plane reached, the command says the checks are not run.
2. **`node replace` plans before the Worker leaves.** The order is now: plan `-replace` with a seeded host key and check it against the intent (exactly one replace); run the gates; retire the Worker (drain, delete Node, remove hub peer); drop its pins; apply the saved plan; pin the seeded key; build and join. A refused plan leaves the Worker whole. `tf.Decide` is split into `tf.Check` and `tf.ApplyChecked`, so the saved plan is applied without a second plan. Terraform refuses it if the workers state changed since.
3. **The Worker-set commit gate warns.** When the checked-out branch has no upstream, or the upstream (as of the last fetch) lacks the set's last commit, the gate warns. It never refuses. The git calls are read-only.
4. **A resumed seeded host key rotation finishes the final pin.** The Probe is done only when the rotated marker exists and exactly one key is pinned at the address. A run resumed after the install skips generate and install, dials accepting only the key pinned last, then pins it alone. It never reads the key from the host. If the host does not serve that key, the pins stay and the run fails.
5. **SIGHUP closes bootstrap SSH.** `main.go` catches SIGHUP with SIGINT and SIGTERM, so a closed terminal still runs the deferred close. `cp init`, `node add`, `node remove`, `node replace` and `reset` warn at start for each firewall that admits port 22.
6. **A sudo read that cannot run says so.** `local.ReadFile` tries `sudo -n true` first and asks with `sudo -v` only when a terminal exists. "File missing" is reported only when `sudo test -e` exits 1 with empty stderr; other failures show sudo's stderr.
7. **The kubeconfig merge works on a copy.** The three deletes run on a copy, the merge is flattened from the copy and the new file, and the live file is written once. A run that stops earlier leaves it unchanged. When the live current-context already is `tosak-admin@tosak`, that name stays current and points at the new cluster's entry from the write on, before `/readyz` answers.

The old kubeconfig test fixture named the old context wrongly, so `delete-context` was never tested. The fixed test asserts three deletes on the copy and none on the live file.

Two limits are kept, with the operator's approval of the PR. SIGPIPE is not caught: with piped output (`| tee`) and a closed terminal kluster can die before the SSH close, and the start warning is the safeguard. The database gate also blocks removing or replacing a dead Worker that holds a not-ready CloudNativePG instance until CloudNativePG fails over or the instance is fenced.

Checks: `gofmt -l` is clean; `go vet ./...` and `go test -count=1 ./...` pass. The `KLUSTER_TF_TEST` tests were not run (terraform rule). Nothing was run against a cluster, so none of the behaviour is proven live.
