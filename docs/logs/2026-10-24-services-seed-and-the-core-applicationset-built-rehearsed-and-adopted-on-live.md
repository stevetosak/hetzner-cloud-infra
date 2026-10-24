---
title: "services seed and the core ApplicationSet built, rehearsed and adopted on live"
description: "What the services seed task did from the CI write path to the live adoption of core/ under ArgoCD, what the rehearsal found, and what stays open."
type: log
date: 2026-10-24
topics: [gitops, kubernetes, security]
---

This task finished the Phase 6 item "`services seed` and the `core` ApplicationSet" of the [rebuild Plan](../runbook/rebuild-2026-09-20.md), as decided in [ADR 0010](../adr/0010-argocd-syncs-core-and-kluster-only-seeds-it.md#amendments). It ran from 2026-10-22 to 2026-10-24 in seven sections. After it, ArgoCD syncs `core/` on live and `kluster services seed` is the one command that installs and repairs it.

## The design and the order of the work

A 14-question review on 2026-10-22 settled the design; the operator chose each answer. The order comes from a seed, not from sync waves. Progressive Syncs turn automatic sync off, so the seed applies the CRDs, Namespaces, Secrets and ArgoCD first, and the core Applications converge on their own. Section 0 wrote the ADR 0010 and ADR 0009 Amendments. Because write access to `master` becomes cluster-admin once `core/` is synced, the CI write path was fixed before anything else (sections 1 and 2).

## The CI write path

Section 1 proved the GitHub mechanics on a throwaway public repository. A ruleset with an empty bypass list binds the owner of a personal repository: a direct push and `gh pr merge --admin` were both refused (`GH013`, "Required status check deploy-scope is failing"). A `pull_request_target` check satisfies the ruleset, a pull request opened with an App token does start workflows, and auto-merge merged an App pull request that changed only `newTag:`. An App pull request that also changed `README.md` failed the check and stayed open.

Planning section 2 found a gap: `deploy-scope` passes human pull requests, and any token with Contents write can push to an open human branch and merge it. The operator chose to sign commits. On a human pull request the check demands that every commit is verified and that author and committer are the pull request author. The proof repository showed that identity fields are fully spoofable and that the signature is the only separator: a `git` push with faked identity, and API commits with author or author and committer set to the operator, all arrived `unsigned` and failed. An App commit with no author set stays `verified`.

Section 2 then built the path on `master`. The check, `.github/scripts/deploy-scope.mjs`, checks out the base commit only. It accepts a human pull request from the repository owner alone, and it refuses a fork pull request. The `tag-bump` action edits only the `newTag:` line, commits through the API and waits for the merge, so a refused bump turns the app's CI red. The deploy catalog data moved to the branch `deploy-catalog`, written with the workflow's own `GITHUB_TOKEN`; a payload now reaches the workflow through `env` only, because the old heredoc with `${{ }}` was an injection path. Rulesets `master-protection` (25714596) and `deploy-catalog-protection` (25714595) have empty bypass lists, and the classic protection on `master` was deleted. End to end, doma pull request 84 produced infra pull request 48, which `deploy-scope` passed and auto-merged; ArgoCD rolled doma to the new image and `deploy-catalog` pushed its first real row. All five Applications now deploy through `tag-bump`. The old secrets and variables were deleted from the three repositories. The operator lets the old personal access tokens expire (latest December 2026) instead of revoking them, because it is unclear which token is which.

## The shape of `core/` and the seed

Section 3 (pull request 53) gave `core/` the shape the seed needs: `crds.yaml` split from each render, one `kustomization.yaml` per synced directory, the `core` AppProject, a second ApplicationSet `core-applicationset.yaml`, and `kluster services check` with CI. The check treats the `projects` generator list as the source of truth for AppProject, namespace file, `allowedRoutes`, `apps.json` and Reloader namespaces. A migration proof compared the old and new object sets: of 227 objects, 208 land unchanged and 19 are accounted for. The rehearsal cuts live at `core/rehearsal/<dir>`, not `core/<dir>/rehearsal`, because kustomize refuses an overlay inside its own base ("cycle detected"); ADR 0009 is corrected.

Section 4 (pull request 54) built `kluster services seed` in the stages `seed-crds`, `seed-namespaces`, `seed-secrets`, `seed-argocd`, `core-healthy`, `lb-address` and `zone`. ArgoCD parts are built with kustomize 5.8.1 (the version of ArgoCD v3.5.3) from the revision each environment reads. 🔴 ArgoCD v3.5.3 adds a `tracking-id` annotation before it applies; the seed adds the same value, or every seed run would strip an object from ArgoCD's ownership. The zone Stage reads Cloudflare with a read-only token (Bitwarden `kluster/cloudflare-zone-read`; proven: the zone and A-record calls succeed, a POST to `dns_records` gives HTTP 403) and never writes DNS. Plan Mode on live ran with the operator's yes and returned rc 0; its first run showed that `-n <destination>` made kubectl refuse objects in other namespaces, so the seed passes no `-n`.

## What the rehearsal found

Section 5 ran a rehearsal from an empty project, with seed runs in Plan Mode and with `--apply`, and tore it down; the project was empty on two read-backs. The branch `rehearsal` is the revision a rehearsal reads.

- **Hook Jobs are idempotent.** The `argocd-redis` Secret kept its creation time over four or more syncs, the envoy Secrets and pods were unchanged after a forced resync, and `cert-manager-startupapicheck` succeeded on a second forced sync.
- **`argocd-secret` survives.** `admin.password`, `admin.passwordMtime` and `server.secretkey` remained after repeated server-side applies.
- **A ScheduledBackup and a Database wait for a missing Cluster** without error. However, `immediate: true` leaves a Backup in phase `pending` that starts when a Cluster appears; `db create` must account for it.
- **Break and repair.** Deleting `argocd-repo-server` was not restored by ArgoCD within 60 seconds; the seed found two differing objects, applied them, and `core-healthy` reached 6 of 6.
- **Zone report.** The mismatch report read `argocd.tosak.net: A 77.42.14.48; change it to 2.29.114.65`, and the Stage refused as designed.
- 🔴 **Gateway and HTTPRoute stayed OutOfSync for ever** under ArgoCD's legacy diff, because the API fills defaults inside list items. `compare-options: ServerSideDiff=true` gives Synced, but the ApplicationSet strips a hand annotation, so the annotation is now in the core ApplicationSet template (commit 7bafa0f); the first compare after the fix needed one hard refresh.
- 🔴 **`core-reloader` failed in a rehearsal**: Roles in `authos` and `doma` had no namespace (`namespaces "doma" not found`, retried 10 times). A reloader rehearsal cut fixes it, and the seed now refuses an object in a namespace the environment does not create (6fc406a).
- 🔴 **Primary IPs are counted per account.** The rehearsal project is not isolated from live in this: the rehearsal load balancer was refused with `Primary IP limit exceeded (resource_limit_exceeded)` while the account held 9 of 10. A load balancer holds two Primary IPs (the `down` listing showed `primary_ip-157515610` and `-611`). Hetzner raised the limit to 15 the same day. `kluster down` also races the unassignment of the load balancer's Primary IP (`must_be_unassigned`); a second run empties the project. Both became Plan items.

One Plan item was not checked in this rehearsal: the resume of a half-failed kubeadm init and root SSH on the Control Plane.

## Live adoption

Section 6 ran the seed on live in Plan Mode with `--diff-dir`, and the operator reviewed every diff. Beyond the tracking annotation, `cnpg`, `pg-drill`, `redis` and `reloader` showed none. `gateway` showed only the certgen hook Job, `cert-manager` only the leader-election namespace change from `kube-system` to `cert-manager` (with two Roles and two RoleBindings), and `argocd` only the new `core` AppProject and ApplicationSet and a hook Job. With the operator's approval, `kluster services seed --env live --apply` ran. Proven by `kubectl get applications`: all 12 Applications Synced and Healthy, `core-healthy` 7 of 7; the ClusterIssuer and 3 Certificates Ready; the new cert-manager pods hold their Leases in `cert-manager`. The load balancer address is 77.42.14.48 and the zone Stage found 5 hosts matching. Then, with the operator's approval, the old Roles, RoleBindings and two holder-less Leases were deleted by hand in `kube-system`, because prune is off; the read-back found none left.

One transient `ACME client for issuer not initialised` appeared at pod start. Of the hosts, argocd, doma, authos and authos-demo answered 200; `authos-api` answered 404 on `/`, `/health` and `/actuator/health` with a JSON content type from the app. The seed touched nothing in the `authos` namespace, so this is assumed to predate the seed, not proven.

## Still open

The NAT gateway for the private network (after the rebuild) and the `kluster down` race are Plan items from pull request 55. The deferred items are DNS writes, the deploy catalog outside git, and a `kluster project add` scaffold command. The Authos `e2e.yaml` concurrency fix merged without a test of several deploy workflows at once, and the catalog push loop under parallel runs is not exercised. The branch `fix/kluster-guided-pass-gaps` still waits for push approval.

## Lesson candidates

- [x] **A separate project, namespace or environment isolates only what its provider scopes to it; check which quotas the boundary still shares with live before a design relies on it.** — [Lesson](../lessons/an-isolation-boundary-is-checked-for-the-quotas-it-still-shares-before-a-design-relies-on-it.md)
