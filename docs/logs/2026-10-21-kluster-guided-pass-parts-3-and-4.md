---
title: "kluster guided pass: parts 3 and 4 walked, nine findings"
description: "What the last two parts of the guided pass over the risky kluster code covered, how the symptom step ended, and the findings the pass produced."
type: log
date: 2026-10-21
topics: [provisioning, security]
---

This task finished the Phase 6 item "Walk the operator through the risky kluster parts (a guided pass)" in the [rebuild Plan](../runbook/rebuild-2026-09-20.md). Parts 1 and 2 were walked on 2026-10-20 ([Log](2026-10-20-kluster-guided-pass-parts-1-and-2.md)); parts 3 and 4 were walked on 2026-10-21, so the item is done. The records are working material and are not published; this Log keeps the outcome.

## What the parts covered

Part 3 covers Worker add, remove and replace: `cmd/node_add.go`, `cmd/node_remove.go`, `cmd/node_replace.go`, `internal/workerset`, `internal/stages/worker_*` and `internal/provision/workers.go` under `tools/kluster/`. Its 12 decisions run from the Worker set written by kluster and committed by the operator, through the drain, the Node delete and the removal of the VPN peer, to the gates of `replace` and the pin handling.

Part 4 covers the Control Plane build, trust and the laptop: `cmd/cp.go`, `cmd/pin.go`, `internal/hostkey`, `internal/sshgate`, `internal/stages/hostkey.go`, `internal/stages/laptop_*`, `internal/local` and `internal/routeproof`. Its 12 decisions run from the seeded host key and its rotation, through the bootstrap SSH window on the Control Plane firewall, to the kubeconfig merge and the route proof.

## How the symptoms went

In part 3 the operator diagnosed the first symptom in part. A drain blocked by a PodDisruptionBudget was met with the right action but for the wrong reason. The second symptom, a `replace` refused after the retire Stages, was not diagnosed. From part 4 on the operator dropped the symptom step, and the decisions were answered in short form.

## Findings

All findings come from reading code and docs, spot-checked by path and line on `f969fed`. Nothing was run against a cluster.

1. **The ADR describes the commit gate wrongly.** [ADR 0009](../adr/0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md) said the Worker-set gate applies "Under `--apply`". The code gates in Plan Mode too (`tools/kluster/cmd/root.go:118-127`, no mode check), which is the intended behaviour. The ADR is corrected.
2. **`node remove` has no database gate and no last-Worker check.** Only `replace` and `reset` have them (`cmd/node_replace.go:161-181`).
3. **`node replace` retires before it plans.** The Node, the hub peer and the pins are retired first (`cmd/node_replace.go:97-117`), so a refused plan leaves a half-retired Worker.
4. **The commit gate checks a local commit only.** A commit that is not pushed passes, although another clone reads the set from the remote.
5. **A stopped seeded-key rotation keeps both pins.** The "rotated" marker is written inside the install script (`internal/stages/hostkey.go:31-37`), before the final `Pins.Set` (`:97`). A run stopped in between is skipped on resume, and the seeded key stays pinned beside the new one. [The host keys page](../runbook/host-keys.md) is corrected.
6. **The bootstrap SSH close runs on SIGINT and SIGTERM only** (`main.go:16`). A closed terminal (SIGHUP), SIGKILL or a crash leaves port 22 open to the laptop /32 on the Control Plane firewall.
7. **The `pin import` check is not independent.** The documented `ssh-keygen -lf` check goes through the same `~/.ssh/known_hosts` entry that is imported. The independent source is the host key fingerprint in the Hetzner console boot log. The host keys page now says so.
8. **A sudo failure reads as "file does not exist".** `local.ReadFile` turns any exit 1 of `sudo test -e` into a missing file (`internal/local/local.go:48-51`), and the sudo path has no test.
9. **The kubeconfig merge edits the live file before the write.** It runs `delete-context`, `delete-cluster` and `delete-user` on `~/.kube/config` before the merged file is written (`internal/stages/laptop_kubeconfig.go:111-139`).

Two points are not verified: the resume of a half-failed `kubeadm init` (there is no `kubeadm reset` path, `internal/stages/cp_kubeadm.go`), and whether root SSH stays enabled on the Control Plane after the build (`internal/stages/cp_base.go:34-35`). Both go to the next rehearsal.

One open question of part 3, that hand-built live Workers have no kluster pin, needs no new step. The live Control Plane pin was imported on 2026-10-06 ([host keys](../runbook/host-keys.md#a-server-kluster-did-not-build)), and that pin is what the `node` commands log into. `node add` refuses a hand-built Worker server by design (`cmd/node_add.go:103-111`); [the Workers runbook](../runbook/workers.md) now says it is rebuilt with `node replace`.

The operator decided 11 gaps, all as recommended. The code fixes for findings 2, 3, 4, 5, 6, 8 and 9 follow on a separate branch, `fix/kluster-guided-pass-gaps`, not merged at the time of writing.

## Pages changed

The Plan ticks parts 3 and 4 and the guided-pass item, and gains two open items. ADR 0009 has one factual correction, with no amendment section. The host keys page and the Workers runbook each gain a short note.

## Lesson candidates

None.
