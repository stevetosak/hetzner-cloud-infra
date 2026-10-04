---
title: "ArgoCD syncs core/, and kluster only seeds it"
description: "Why the cluster services in core/ move from hand-applied to synced by ArgoCD, what kluster still applies itself, and why the database Cluster stays out."
type: decision
status: accepted
date: 2026-10-04
topics: [kubernetes, gitops, databases]
---

Today ArgoCD syncs only the project overlays, through the one ApplicationSet
of [ADR 0003](./0003-recoverability.md). Everything in `core/` — the Gateway API CRDs, Envoy Gateway,
cert-manager, CNPG, Redis, ArgoCD itself, the backup and the Restore Drill —
was applied by hand, in the order of
[the cluster services runbook](../runbook/cluster-services.md), which runs to seventeen sections.

[ADR 0009](./0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md) makes kluster build the whole cluster, services included. Porting each
of those sections as a kluster Stage would make kluster a second deployment
system beside ArgoCD, with its own idea of what is live. Handing `core/` to
ArgoCD instead changes a rule written in the workspace guide and both
`CLAUDE.md` files, so the reasons are recorded here.

## The decision

**ArgoCD syncs `core/`; kluster seeds only what has to exist before ArgoCD
can.** A second ApplicationSet covers `core/`, with sync waves for the order
the runbook proved: Gateway API CRDs, Envoy Gateway, cert-manager, the CNPG
operator, Redis, and onwards. kluster's `services seed` applies the CRDs that
must precede it, the SOPS Secrets, and ArgoCD itself with both
ApplicationSets. A few steps stay imperative kluster Stages because no
manifest expresses them: the Cloudflare zone, and anything that waits on the
load balancer's address.

**Secrets stay out of ArgoCD.** They are applied by kluster from the
`*.enc.yaml` files, as `scripts/secrets.sh apply` does today; there is still
no ArgoCD plugin ([the secrets runbook](../runbook/secrets.md), "What is deliberately NOT done").

**The CNPG `Cluster` resource stays out of ArgoCD.** The committed
`core/cnpg/pg-cluster.yaml` keeps `bootstrap.initdb` on purpose; a recovery
applies a patched copy that is never committed ([the recovery runbook](../runbook/pg-recovery.md)).
If ArgoCD synced that file after a total loss, it would create an empty
database at once, the applications would start writing to it, and self-heal
would turn a recovered Cluster's `serverName` back to the old generation.
So kluster creates the Cluster as a Stage, and only on an explicit
`--db fresh` or `--db recover`. There is no detection: a failed bucket read
must never be taken to mean "nothing to recover".

## Why this and not the alternatives

Porting every section as a Stage keeps the out-of-band rule intact and maps
the runbook one to one, at the price of the largest port and a deploy system
that duplicates ArgoCD. A hybrid — stateless controllers to ArgoCD, stateful
steps to kluster — would leave two models to learn for one directory.

For the database, committing the recovery manifest before seeding would be
pure GitOps, but it needs a commit and a merge in the middle of a disaster and
leaves an empty database one mistake away. A manual-sync annotation on the
Cluster would keep it visible in ArgoCD, and a single click could still sync
the `initdb` manifest over a recovered cluster.

## What it costs

Drift in `core/` becomes visible and self-healing, which is the point, and a
wrong commit to `core/` now reaches the cluster without a hand in between.
The ApplicationSet must list its `core/` paths the way the project generator
lists projects, so nothing unsynced on purpose is adopted. `core/argocd/`
syncing itself needs care so that a broken commit cannot remove the
controller that would repair it.

The workspace guide's and both `CLAUDE.md` files' lists of what is applied
out of band change when this is built, not before: until then, they describe
the cluster as it is. Worth revisiting if sync waves cannot express an order
the runbook depends on, or if an ArgoCD outage ever blocks a recovery.
