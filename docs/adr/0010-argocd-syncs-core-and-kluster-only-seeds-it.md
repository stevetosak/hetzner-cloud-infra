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
`*.enc.yaml` files, with `kluster secrets apply`; there is still
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

## Amendments

**2026-10-22 — the order comes from a seed, not from sync waves, and write
access to `master` is narrowed first.** A design review before the build
answered fourteen questions the decision above left open. Each answer was
chosen by the operator. Nothing below is built yet, and the GitHub mechanics
in the last part are still to be proven.

The decision named sync waves for the order. Waves order resources inside one
Application; they cannot hold one Application back until another is healthy,
and the ApplicationSet feature that can, Progressive Syncs, turns off
automatic sync for the Applications it manages (behind
`applicationsetcontroller.enable.progressive.syncs`, off in chart 10.9.2). An
app of apps would add a layer that has to be seeded too. The order instead
comes from the seed: `kluster services seed` applies every CRD server-side
first, then ArgoCD and both ApplicationSets, and the core Applications
converge on their own with `SkipDryRunOnMissingResource=true` and `retry`. An
Application whose kind is not served yet fails and retries until it is.

**Only kluster owns CRDs.** Each `render.sh` writes the CRDs of its chart to
`core/<dir>/crds.yaml`, which no `kustomization.yaml` lists, and the seed
applies every such file. `CustomResourceDefinition` is not in the `core`
AppProject's whitelist, so ArgoCD cannot change or delete one. An upgrade
that brings new CRDs is a seed from the branch, then the merge; kluster warns
when a branch `crds.yaml` differs from the live CRDs. `core/gateway-api/`
holds CRDs only, so it is seeded and is not an Application. The seed also owns
every Namespace, those of `core/` and of the generator's projects, from the
committed files; ArgoCD owns none. `cnpg-system` is cut out of the CNPG
render with a kustomize `$patch: delete` into its own namespace file.
`kube-flannel` stays with `cp init`.

The second ApplicationSet lists its directories the way the project
generator lists projects, and each listed directory becomes the Application
`core-<dir>`. Each synced directory has a `kustomization.yaml` with an
explicit `resources:` list, so a file appears on the cluster only when it is
named, and a check refuses a list that names an `*.enc.yaml`,
`pg-cluster.yaml` or a values file. The synced directories are `gateway`,
`cert-manager`, `redis`, `reloader`, `pg-drill`, `cnpg` and `argocd`.
`hcloud` and `cni` stay with `cp init`, and `monitoring` stays parked. One
AppProject, `core`, admits this repository and the core namespaces by name;
its cluster-scoped whitelist is exactly ClusterRole, ClusterRoleBinding,
MutatingWebhookConfiguration, ValidatingWebhookConfiguration, GatewayClass,
ClusterIssuer and StorageClass. `core-cnpg` syncs the operator, the
barman-cloud plugin, the StorageClass, the ObjectStore, the ScheduledBackup,
and the `Database` objects of the generator's projects only (today doma). The
`Cluster` and `recovery/` stay out, as decided above; that a ScheduledBackup
and a `Database` wait for a missing `Cluster` without harm is to be proven in
the rehearsal.

ArgoCD manages itself in full: `core-argocd` syncs its install, its route,
the AppProjects and both ApplicationSets. The cost named above, a broken
commit removing the controller that would repair it, is met in two ways. The
core ApplicationSet sets `preserveResourcesOnDeletion: true`, and the repair
path is the seed itself, which installs ArgoCD again over whatever is left.
Breaking ArgoCD on purpose and repairing it with the seed is part of the
rehearsal.

**Core Applications self-heal but never prune.** A wrong deletion in `core/`
should leave the object running and show it as out of sync, not remove a
controller. The core ApplicationSet ignores differences in
`.spec.syncPolicy`, so an operator who pauses one Application by hand is not
overruled by the generator a moment later; the runbook names the pause and the
resume. 🔴 The consequence: a change to the template's `syncPolicy` never
reaches an Application that already exists. It takes deleting the
Application, which `preserveResourcesOnDeletion` makes safe. The Reloader
ignore rule of the project generator is copied over; the `projects`
ApplicationSet's own policy does not change.

**After this task, write access to `master` is cluster-admin.** Once `core/`
is synced, a commit to `master` can grant any ClusterRole the whitelist
allows. Today the five deploy workflows of Authos and doma push to `master`
with a personal access token, and protection on `master` (read from the GitHub
API on 2026-10-22) requires a pull request with no approvals, no checks, and
does not bind administrators. So the write path is narrowed in this task,
before `core/` is synced on live, and not later. A GitHub App
`tosak-tag-bump`, installed on this repository only with contents and pull
request write, replaces `INFRA_REPO_TOKEN`: each deploy pushes a branch
`deploy/<app>-<sha>`, opens a pull request and enables auto-merge. A ruleset
on `master` requires a pull request and the check `deploy-scope`, has an
empty bypass list, and forbids force pushes and deletion. `deploy-scope` runs
on `pull_request_target` without a checkout and refuses a bot pull request
that changes anything but `newTag:` in a generator overlay's
`kustomization.yaml`. The deploy catalog's data, `history.jsonl` and
`CATALOG.md`, moves to its own branch `deploy-catalog`, written with the
workflow's own `GITHUB_TOKEN`, while its scripts stay on `master`; the
Deployment record on each app repository is written by a second App,
`tosak-deploy-status`, installed on Authos and doma with Deployments write
alone, instead of `APP_DEPLOY_PAT`. Two Apps, because an App holds the same
permissions on every repository it is installed on: one App on the app
repositories would carry contents write there as well, and whoever holds its
private key can make a token with every permission it has, whatever a
workflow narrows its own token to. **Each of these mechanics is to be proven on a
throwaway public repository first:** that a ruleset with an empty bypass
binds the owner of a personal repository, that a `pull_request_target` check
satisfies it, and that auto-merge works for a pull request opened by an App.

**The seed reads the outside world but does not write it.** It waits for the
load balancer's address and reads the Cloudflare zone; it refuses to report
success while an in-scope A record points elsewhere, and prints the records
to change. Writing those records — a kluster Stage, or `external-dns` in
`core/` — is a separate decision, deferred.

**The live cluster is adopted, not rebuilt.** Everything in `core/` already
runs, applied by hand. In Plan Mode the seed prints a server-side
`kubectl diff` per Application and the diffs of the CRDs, Namespaces and
Secrets; every live diff is fixed or understood before `--apply`. A rehearsal
from an empty project runs first. Two things are to be proven there: that the
Helm hook Jobs in the renders (`eg-gateway-helm-certgen`,
`argocd-redis-secret-init`, `cert-manager-startupapicheck`), which ArgoCD runs
on every sync, are idempotent, and that the keys in `argocd-secret` survive a
server-side apply of a render that carries the Secret with no data. How the
rehearsal cuts its links to the live outside world changed with this design;
[ADR 0009, Amendments](./0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md#amendments)
has it.

Fixing the write path after the cutover was weighed and rejected: it would
leave a personal access token with cluster-admin reach in between. Moving the
deploy catalog out of git entirely, to GitHub Deployments or R2, is deferred
until after the rebuild.

**2026-10-24 — the task is built, and the "to be proven" items are proven.**
The Amendment of 2026-10-22 said the seed was designed and its GitHub and
rehearsal mechanics unproven. The task is done; this records what changed from
the design and what the proofs showed. The [Log](../logs/2026-10-24-services-seed-and-the-core-applicationset-built-rehearsed-and-adopted-on-live.md)
has the evidence, and the [services seed runbook](../runbook/services-seed.md)
the steps.

**The revision rule.** The seed builds the ArgoCD parts from the revision each
environment reads: `origin/master` on live, and on a rehearsal the branch
`rehearsal`, which the rehearsal cut of the core ApplicationSet sets as
`targetRevision` (the operator pushes it with `git push -f origin HEAD:rehearsal`).
CRDs, Namespaces and Secrets come from the working tree. A seed-only patch of
`targetRevision` could not hold, because `core-argocd` syncs the ApplicationSet
file back from git. Objects that ArgoCD syncs are applied as the manager
`argocd-controller`, with force, as ArgoCD does, and carry its `tracking-id`
annotation; the kustomize build is pinned to 5.8.1, the version in ArgoCD v3.5.3.

**The zone token.** The zone Stage reads Cloudflare with a read-only token,
`kluster/cloudflare-zone-read` (Zone:Read and DNS:Read on `tosak.net`), in both
environments. A POST to `dns_records` with it gives HTTP 403. The Stage refuses
success on a mismatched in-scope A record by design, so it also refuses in a
rehearsal, which proves the report. DNS writes stay deferred.

**Rehearsal cuts** are overlays at `core/rehearsal/<dir>`, not
`core/<dir>/rehearsal` ([ADR 0009, Amendments](./0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md#amendments)).
A Secret marked `rehearsal: omit` in `secrets.yaml` is not made in a rehearsal.

**Server-side diff for the core Applications.** ArgoCD's legacy diff kept the
Gateway and HTTPRoute OutOfSync for ever, because the API fills defaults inside
list items. The core ApplicationSet template now carries
`compare-options: ServerSideDiff=true`; an annotation set by hand is stripped
by the generator, so it has to live in the template.

**Proven in the rehearsal (2026-10-24):** the hook Jobs are idempotent; the
keys of `argocd-secret` survive repeated server-side applies; a `ScheduledBackup`
and a `Database` wait for a missing Cluster without error (but `immediate: true`
leaves a Backup `pending` that starts with the Cluster); a broken ArgoCD
(`argocd-repo-server` deleted) is repaired by the seed; the LB mismatch report
works. **Proven on the GitHub proof repository (2026-10-23):** a ruleset with an
empty bypass binds the owner, a `pull_request_target` check satisfies it, and
auto-merge works for an App pull request. The write path went further than the
design: the check also demands signed commits on human pull requests, because
any token with Contents write could otherwise push to an open human branch. The
catalog branch is `deploy-catalog`; the two Apps are `tosak-tag-bump` and
`tosak-deploy-status`.

**Live adoption (2026-10-24):** `kluster services seed --env live --apply`
left 12 Applications Synced and Healthy. The core Applications self-heal and
never prune, so the old cert-manager Roles, RoleBindings and Leases in
`kube-system` were deleted by hand
([runbook](../runbook/services-seed.md#adopt-the-live-cluster)).
