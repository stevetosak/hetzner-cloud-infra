---
title: "Kluster grill: ADR 0009 and 0010, and a rewritten Phase 6"
description: "A grill session on where kluster stands decided that it builds the whole cluster, and the decisions went into two new ADRs and five new Plan steps."
type: log
date: 2026-10-04
topics: [provisioning, gitops, secrets]
---

The session resumed a waiting handoff, and the operator chose the Phase 6 kluster work, run as a `/grill-with-docs` session on where kluster stands and how to develop it. No Plan step was finished. The Plan [Cluster rebuild — 2026-09-20](../runbook/rebuild-2026-09-20.md) changed shape instead: the Phase 3 "Carried into Phase 6" port step for the workers runbook and the two old Phase 6 kluster steps are struck through as replaced, and five new Phase 6 kluster steps stand in their place.

## What pull request 10 holds

PR #10 (`feat/kluster-cli`, commit `c08655e`, 2026-09-13) is one commit of about 3,400 lines of Go, 123 commits behind `master`. It was read in a scratch copy made with `git archive`. `go build ./...`, `go vet ./...` and `go test ./...` all pass, but the tests run against fakes only, so they prove the code compiles and agrees with itself, not that it works against Hetzner.

The branch targets the world before the rebuild. It assumes a single Terraform root in `infra/`, the `node_suffix` and `liveNamesByTfvarsKey` naming, a `LonghornStage`, a `tf.Apply` with no plan step and one `allow_public_ssh` switch. Its `rebuildControlPlaneWireGuard` regenerates the hub config (the runbook's defect 2), it reaches the control plane over the public IP as root, and it pins Kubernetes `v1.34` and a CNI tarball. Proof: `git show origin/feat/kluster-cli:tools/kluster/...` reads and a `grep` of `cmd/*.go`.

Further findings from the same read:

- `remote/ssh.go`, the file chosen for harvesting, uses `ssh.InsecureIgnoreHostKey()`. This must change before the file is reused.
- `master` tracks `tools/kluster/kluster.yaml` (updated in `840fc4d` for ADR 0006) while the branch's `.gitignore` ignores it. The committed file stays.
- An ignored, stale binary `tools/kluster/kluster` (2026-09-13, old `-auto-approve` code) sits in the main checkout. It was not deleted; the operator decides.

## Decisions

Twenty questions were put to the operator, who chose each answer. The decisions are recorded in [ADR 0009](../adr/0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md), [ADR 0010](../adr/0010-argocd-syncs-core-and-kluster-only-seeds-it.md) and the 2026-10-04 amendment to [ADR 0003](../adr/0003-recoverability.md); this Log does not repeat their reasoning. In short:

- kluster owns the Workers and the Control Plane. `cp init` runs only into an empty slot.
- The work starts on a fresh branch from `master`. The `remote/` package is harvested, with a Stage and Runner seam and errgroup fan-out, and PR #10 is closed.
- The runbook leads and each Stage cites it. Plan Mode covers hosts too, through a read-only SSH probe and diffs. A Stage is a Probe plus an Act, and can resume.
- Closing SSH goes through Terraform, an API readback and `set_rules []`. kluster writes the tfvars and a commit gate runs before apply. A rolling Reset uses `node replace`.
- Automation reaches the services too, conditional on secrets. SOPS stays and Bitwarden is the root at run time, through the `bw` CLI. Secrets split into Generated, Issued and Derived.
- ArgoCD syncs `core/`, with the CNPG `Cluster` and the Secrets excluded and a `--db fresh|recover` choice.
- Each build gets a new hub WireGuard key, and kluster edits the operator's `wg0.conf`. Host keys are seeded through cloud-init `user_data`.
- The commands stay small, with `kluster up` on top. A rehearsal runs in a second Hetzner project (`--env rehearsal`), is torn down with `kluster down`, and the watcher checks its age. The rehearsal patch set cuts Let's Encrypt, Cloudflare, ArgoCD notifications and R2 writes.
- Build order: safety core, `cp init`, Workers, secrets and services, then `up`.

The docs went onto branch `docs/kluster-grill` (`7128b7e`): the two ADRs, a status line and an Amendments section in ADR 0004, the ADR 0003 amendment, `CONTEXT.md` (Plan Mode covers hosts, the Bootstrap runbook comes first, new terms Stage, Replace, Generated Secret, Issued Secret and Derived Credential, Reset is rolling) and the rewritten Plan steps. `npm run check -- cloud-infra --root <work tree>` passed, and `npm run build` with `TOSAK_SOURCES_DIR` pointing at the work tree passed with 53 pages.

## Faults the design now avoids

- Adding `user_data` to a live `hcloud_server` forces a replacement. Both Modules need `lifecycle.ignore_changes = [user_data]` before host-key seeding.
- An uncommitted tfvars Worker entry reads as a planned destroy from any other clone, because the state is shared in R2. The commit gate answers this.
- ArgoCD syncing `core/cnpg/pg-cluster.yaml` after a loss would create an empty initdb database, and self-heal would revert a recovered `serverName`. The Cluster therefore stays out of ArgoCD.

## Rehearsal cost

A read-only `GET /v1/pricing` on 2026-10-04 (EUR net, hel1) gave cx23 at 0.0088 per hour, lb11 at 0.0120 per hour, a primary IPv4 at 0.0008 per hour and volumes at 0.0572 per GB per month. That is about 0.05 per hour, so one rehearsal costs 0.10 to 0.25 and a forgotten one about 32 per month. No Hetzner spending cap is known; this is assumed, not checked.

## Open

- `TF_VAR_WG_REMOTE_PUBLIC_KEY` and `_PRIVATE_KEY` in `infra/.envrc` have no reader: `grep -rn WG_REMOTE infra tools docs` is empty. Only the variable names were read.
- The operator decides on deleting the stale `tools/kluster/kluster` binary.
- PR #10 closes with a link at the start of chunk 1. `bw` and `bws` are not installed.
- Chunk 1 is the kluster safety core on a fresh branch from `master`. Libraries to propose there: `hcloud-go`, `terraform-exec` with `terraform-json`, cobra (kept) and the `bw` CLI through exec.
