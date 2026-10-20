---
title: "kluster guided pass: parts 1 and 2 walked, four findings"
description: "What the first two parts of the guided pass over the risky kluster code covered, how the operator did on the symptoms, and the four findings the pass produced."
type: log
date: 2026-10-20
topics: [provisioning, secrets, security]
---

This task started the Phase 6 item "Walk the operator through the risky kluster parts (a guided pass)" in the [rebuild Plan](../runbook/rebuild-2026-09-20.md). The item came out of the 2026-10-10 decision to recover on touch and to give the operator one pass over the parts of kluster where a mistake costs the most. The pass is split into four parts. Parts 1 and 2 were walked on 2026-10-20; parts 3 (Worker add, remove and replace) and 4 (Control Plane build, trust and laptop) are not done, and the Plan item stays open for them.

## How a part runs

Each part has a record, drafted by a research agent that read the code, the tests and the docs: what the part does in the order it runs, a list of 12 decisions, and two symptoms. A decision names the choice, the alternative, the reason and a source, and the source says whether the reason is recorded in an ADR, a Log or a code comment, or only inferred from the code. A symptom is a fault the operator is asked to diagnose from its description: the cause, the first place to look, a check and the fix. The records are working material and are not published; this Log keeps the outcome.

Part 1 covers environment isolation and the Terraform run: `internal/env`, `internal/config`, `internal/tf`, `internal/intent`, `internal/cloud` and `cmd/exec.go` under `tools/kluster/`. Its decisions run from Plan Mode as the default, through the clean environment of the Terraform child, the private run directory for the Hetzner token and the checks that keep a rehearsal apart from live, to `kluster exec`. Part 2 covers the secrets path: `cmd/secrets.go`, `internal/secrets`, `internal/sops`, `internal/bitwarden` and `internal/kube`, plus Reloader. Its decisions run from the value-never-printed rule, through the resolution order of the catalog, the server-side apply with its read-back and the shape check of `check`, to the opt-in scope of Reloader. Nothing was run against Bitwarden, sops, Hetzner or the cluster; the Go unit tests of the packages in both parts passed, and the Terraform-backed tests, gated by `KLUSTER_TF_TEST=1`, were not run.

## How the symptoms went

In part 1 the operator did not diagnose either symptom, and the agent gave both answers. In the first, a rehearsal run is refused with "this project holds primary IP … it is the live project" after the live Hetzner token was rotated: the item `kluster/rehearsal` still held the old live token, which differs from the new live value and so passes the value comparison, while the project guard reads the project behind the token and finds the live Control Plane's address. In the second, a `TF_VAR_` variable exported in the shell has no effect under `kluster exec`: the clean environment drops every `TF_` variable except the plugin cache, and the value has to be passed after `--` as Terraform's own `-var` flag.

In part 2 the operator reached about half of each answer. For the first symptom, an item stored in the web vault that `secrets pull` reports as "not found", the operator reached "the item is not synced". Two parts were missed: `pull` syncs by itself, and a failed sync only prints a warning before the stale local copy is read; and the name must match exactly. For the second, a Deployment in a new namespace that kept the old Redis password after a rotation, the operator reached "check the annotation and the restarts". The missed part: the namespace must also be listed in `namespaces` of `core/reloader/reloader-values.yaml`, because Reloader runs scoped.

## Findings

1. **The bucket check cannot see the live bucket.** The rule that a rehearsal must not name the live bucket compares `r.StateBucket == l.StateBucket` (`tools/kluster/internal/config/config.go:312`), but the live `stateBucket` is `""` in `tools/kluster/kluster.yaml:106`; the live bucket name sits in `infra/shared/backend.tf:9`. A rehearsal that names `hetzner-cloud-infra` therefore passes the check, although [ADR 0009](../adr/0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md) says configuration refuses it, and no test covers the case. The backstop that remains is the rehearsal R2 token, which is scoped to the staging bucket and would get a 403 on the live one. Found by reading; no config was run through the loader to confirm it.
2. **The rehearsal runbook was stale.** [The rehearsal procedure](../runbook/rehearsal.md) said `--env rehearsal` reads its own environment variables. Since [the 2026-10-20 change](2026-10-20-kluster-environments-read-bitwarden-and-infraenvrc-retired.md) every environment reads a Bitwarden item. The paragraph is corrected in this change.
3. **Nothing warns that a Secret's reader will not restart.** A changed Secret reaches a pod only through Reloader, which acts on Deployments that carry `secret.reloader.stakater.com/auto: "true"` in the namespaces listed in `core/reloader/reloader-values.yaml:32-36`. `kluster secrets diff` exits 0 whether or not a reader meets both conditions, so a new reader is silent until its pod is found old. The condition is written in `core/reloader/README.md` and [the Secrets runbook](../runbook/secrets.md), not enforced.
4. **The old shared Hetzner token is not revoked.** This stays with the operator; `hcloud_token` in `kluster/live` is rotated first. See [the 2026-10-20 Log](2026-10-20-kluster-environments-read-bitwarden-and-infraenvrc-retired.md).

A real fault showed up during the pass. The default `rbw` profile answered `New device verification required`, which `rbw` shows as `failed to log in to bitwarden instance: api request returned error: 400` (HTTP 400, error `device_error`), and `rbw register` fixed it. kluster does not use the default profile; it reads the profile `kluster`, kept in `~/.config/rbw-kluster`. [The Secrets runbook](../runbook/secrets.md) now has the note. The sync failure of this kind is the same one that `secrets pull` only warns about (`tools/kluster/internal/bitwarden/bitwarden.go:108-110`).

## Pages changed

The Plan item is rewritten, not ticked: parts 1 and 2 are marked done with a link to this Log, parts 3 and 4 stay open, and two new open items cover findings 1 and 3. The ADRs are unchanged.

## Lesson candidates

A failed `rbw sync` only prints a warning, and the stale local copy is read after it, so an item stored in the web vault can show as "not found". The warning on stderr is the first thing to read.
