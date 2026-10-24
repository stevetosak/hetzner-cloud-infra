---
title: "The services seed: install, adopt and repair core/ with kluster"
description: "How kluster services seed puts ArgoCD in charge of core/, what each of its seven stages checks, how the live cluster was adopted, and how the seed repairs a broken ArgoCD."
type: procedure
topics: [gitops, kubernetes, provisioning]
verified: 2026-10-24
---

`kluster services seed` applies what ArgoCD cannot apply for itself and then waits until ArgoCD has taken over `core/` ([ADR 0010](../adr/0010-argocd-syncs-core-and-kluster-only-seeds-it.md#amendments)). It runs on the Control Plane over SSH in both environments, through `kubectl` there, and puts Secret values only in `/run/kluster`. Like every kluster write it runs in Plan Mode unless given `--apply`; the operator runs it in their own terminal, because it reads the age key and the Cloudflare token from Bitwarden.

```sh
cd tools/kluster
go run . services seed                              # Plan Mode: what would change
go run . services seed --diff-dir /tmp/seed-diff    # also saves one diff per core Application
go run . services seed --apply
```

Seven stages run in order, and each is skipped when its probe finds nothing to do. The seed builds the ArgoCD parts with kustomize 5.8.1 (the version of ArgoCD v3.5.3) from the revision the environment reads: `origin/master` for live, the branch `rehearsal` for a rehearsal. CRDs, Namespaces and Secrets come from the working tree. Objects that ArgoCD syncs are applied server-side as the manager `argocd-controller`, with force, and carry the same `argocd.argoproj.io/tracking-id` that ArgoCD adds, so a seed run takes nothing from ArgoCD's ownership (🔴 without that annotation every run would strip it).

## seed-crds

Applies every `crds.yaml` in `core/`, server-side. Only kluster owns CRDs: the `core` AppProject does not whitelist `CustomResourceDefinition`, so ArgoCD cannot change or delete one. An upgrade that brings new CRDs is a seed from the branch, then the merge.

## seed-namespaces

Applies every Namespace file: those of `core/` and those of the projects in the `projects` generator list. ArgoCD owns no Namespace. The stage refuses an object whose namespace the environment does not create (a Role in `doma` in a rehearsal that has no `doma`), because ArgoCD would retry it for ever.

## seed-secrets

Decrypts the `*.enc.yaml` files and applies the Secrets. In a rehearsal each Secret of a rehearsal namespace is made fresh in memory, except entries marked `rehearsal: omit` in `secrets.yaml` ([secrets runbook](./secrets.md#1-the-catalog-and-the-keys)).

## seed-argocd

Applies the `core-argocd` parts: the install, the route, the AppProjects and both ApplicationSets. **This stage is also the repair path.** When a commit has broken ArgoCD, the seed installs it again over whatever is left. Proof from the rehearsal on 2026-10-24: after `argocd-repo-server` was deleted by hand, ArgoCD did not restore it within 60 seconds; the stage found two differing objects (the Deployment and the hook Job `argocd-redis-secret-init`), applied them, and `core-healthy` reached 6 of 6. The seed applies hook Jobs as plain objects.

The helm hook Jobs in the renders are idempotent, which is why the seed and ArgoCD may both apply them (proven in the rehearsal on 2026-10-24): the `argocd-redis` Secret kept its creation time over four syncs, the envoy certgen Secrets and pods were unchanged after a forced resync, and `cert-manager-startupapicheck` succeeded again. The keys `admin.password`, `admin.passwordMtime` and `server.secretkey` of `argocd-secret` survive repeated server-side applies.

## core-healthy

Waits until every `core-<dir>` Application is Synced and Healthy (7 on live, 6 in a rehearsal). Two behaviours matter. The core ApplicationSet carries `compare-options: ServerSideDiff=true` on its Applications, because ArgoCD's legacy diff keeps a Gateway and an HTTPRoute OutOfSync for ever (the API fills defaults inside list items). 🔴 The first compare after that annotation arrives needs one hard refresh of the Application; a refresh without it does nothing. And the ApplicationSet ignores `.spec.syncPolicy`, so an operator may pause one Application by hand and the generator does not undo it. The consequence is that a change of the template's `syncPolicy` does not reach an existing Application; delete the Application (`preserveResourcesOnDeletion` keeps its objects) to make it follow.

## lb-address

Waits for the Gateway `tosak` to report its address and takes the one public IPv4 (its `status.addresses` also lists an IPv6 and a private address). On live this was `77.42.14.48`.

## zone

Reads the Cloudflare zone `tosak.net` with the read-only token `kluster/cloudflare-zone-read` (Zone:Read and DNS:Read; a POST to `dns_records` gives HTTP 403, proven 2026-10-24) and compares the A records of the hosts the synced routes use. A record that points elsewhere makes the stage refuse success and print `<host>: A <current>; change it to <address>`. **The stage never writes DNS**; writing is a deferred decision. In a rehearsal this stage refuses by design: it printed `argocd.tosak.net: A 77.42.14.48; change it to 2.29.114.65`.

## Adopt the live cluster

Everything in `core/` already ran, applied by hand, before the first seed. The adoption on 2026-10-24 went in this order.

1. Run Plan Mode with `--diff-dir` and read every diff. Beyond the tracking annotation, `cnpg`, `pg-drill`, `redis` and `reloader` showed no difference; `gateway` showed the certgen hook Job; `cert-manager` the leader-election namespace change; `argocd` the `core` AppProject, the core ApplicationSet and a hook Job. Fix or accept each diff.
2. Run `--apply`. Proof: `kubectl get applications -n argocd` lists every Application Synced and Healthy (12 on live), the ClusterIssuer and the three Certificates are Ready, and the hosts answer.
3. Delete by hand what the old layout left, because `core` never prunes: in `kube-system` the Roles and RoleBindings `cert-manager:leaderelection` and `cert-manager-cainjector:leaderelection` and the Leases `cert-manager-controller` and `cert-manager-cainjector-leader-election` (cert-manager now holds its leases in `cert-manager`). Read them back to prove none is left.

## Rehearse it

A rehearsal reads the branch `rehearsal`, not `master`. Push the branch first, then seed ([rehearsal runbook](./rehearsal.md)):

```sh
git push -f origin HEAD:rehearsal
go run . --env rehearsal services seed --apply
```

The rehearsal cuts are kustomize overlays at `core/rehearsal/<dir>` (self-signed ClusterIssuer, notifications off, the `projects` ApplicationSet and the Restore Drill out, and no Roles for projects that do not exist). Kustomize refuses an overlay inside its own base, so they cannot live in `core/<dir>/rehearsal`.

A `ScheduledBackup` and a `Database` wait for a missing Cluster without error (proven in the rehearsal). 🔴 However, `immediate: true` leaves a Backup in phase `pending` that starts as soon as a Cluster appears, and `db create` must account for it.

## Check before a merge

`kluster services check` (and the CI workflow `services-check.yml`) holds the repository to this shape: every synced directory has a `kustomization.yaml` that names no `*.enc.yaml`, no `pg-cluster.yaml`, no `crds.yaml`, no Namespace and no values file, and every project in the generator list has its AppProject, namespace file, `allowedRoutes` entry and `apps.json` rows.
