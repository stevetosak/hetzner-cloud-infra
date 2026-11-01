---
title: "The services seed: install, adopt and repair core/ with kluster"
description: "How kluster services seed puts ArgoCD in charge of core/, what each of its stages checks, how the live cluster was adopted, and how the seed repairs a broken ArgoCD."
type: procedure
topics: [gitops, kubernetes, provisioning]
verified: 2026-10-25
---

`kluster services seed` applies what ArgoCD cannot apply for itself and then waits until ArgoCD has taken over `core/` ([ADR 0010](../adr/0010-argocd-syncs-core-and-kluster-only-seeds-it.md#amendments)). It runs on the Control Plane over SSH in both environments, through `kubectl` there, and puts Secret values only in `/run/kluster`. Like every kluster write it runs in Plan Mode unless given `--apply`; the operator runs it in their own terminal, because it reads the age key and the Cloudflare token from Bitwarden.

```sh
cd tools/kluster
go run . services seed                              # Plan Mode: what would change
go run . services seed --diff-dir /tmp/seed-diff    # also saves one diff per core Application
go run . services seed --apply
```

Seven stages run in order, and an eighth, `rehearsal-cuts`, joins them in a rehearsal. Each is skipped when its probe finds nothing to do. `kluster up` runs the same stages, with `zone` moved behind the database ([below](#zone)). The seed builds the ArgoCD parts with kustomize 5.8.1 (the version of ArgoCD v3.5.3) from the revision the environment reads: `origin/master` for live, the branch `rehearsal` for a rehearsal. CRDs, Namespaces and Secrets come from the working tree. Objects that ArgoCD syncs are applied server-side as the manager `argocd-controller`, with force, and carry the same `argocd.argoproj.io/tracking-id` that ArgoCD adds, so a seed run takes nothing from ArgoCD's ownership (🔴 without that annotation every run would strip it).

## seed-crds

Applies every `crds.yaml` in `core/`, server-side. Only kluster owns CRDs: the `core` AppProject does not whitelist `CustomResourceDefinition`, so ArgoCD cannot change or delete one. An upgrade that brings new CRDs is a seed from the branch, then the merge.

## seed-namespaces

Applies every Namespace file: those of `core/` and those of the projects in the `projects` generator list. ArgoCD owns no Namespace. The stage refuses an object whose namespace the environment does not create (a Role in `doma` in a rehearsal that has no `doma`), because ArgoCD would retry it for ever.

## seed-secrets

Decrypts the `*.enc.yaml` files and applies the Secrets. In a rehearsal each Secret of a rehearsal namespace is made fresh in memory, except entries marked `rehearsal: omit` in `secrets.yaml` ([secrets runbook](./secrets.md#1-the-catalog-and-the-keys)). An issued key that names a `rehearsalItem:` is read from that Bitwarden item instead, so a rehearsal never holds a live token; the value is not written to an `*.enc.yaml`.

## seed-argocd

Applies the `core-argocd` parts: the install, the route, the AppProjects and both ApplicationSets. **This stage is also the repair path.** When a commit has broken ArgoCD, the seed installs it again over whatever is left. Proof from the rehearsal on 2026-10-24: after `argocd-repo-server` was deleted by hand, ArgoCD did not restore it within 60 seconds; the stage found two differing objects (the Deployment and the hook Job `argocd-redis-secret-init`), applied them, and `core-healthy` reached 6 of 6. The seed applies hook Jobs as plain objects. On a rerun the stage applies that Job again, because its 60-second TTL has deleted it and the probe sees a difference; this is harmless (seen in the reruns of 2026-10-25), and a Plan item makes the stage skip TTL-deleted Jobs.

The helm hook Jobs in the renders are idempotent, which is why the seed and ArgoCD may both apply them (proven in the rehearsal on 2026-10-24): the `argocd-redis` Secret kept its creation time over four syncs, the envoy certgen Secrets and pods were unchanged after a forced resync, and `cert-manager-startupapicheck` succeeded again. The keys `admin.password`, `admin.passwordMtime` and `server.secretkey` of `argocd-secret` survive repeated server-side applies.

## core-healthy

Waits until every `core-<dir>` Application is Synced and Healthy (8 on live, 7 in a rehearsal, since `core-monitoring` joined on 2026-11-01). Two behaviours matter. The core ApplicationSet carries `compare-options: ServerSideDiff=true` on its Applications, because ArgoCD's legacy diff keeps a Gateway and an HTTPRoute OutOfSync for ever (the API fills defaults inside list items). 🔴 The first compare after that annotation arrives needs one hard refresh of the Application; a refresh without it does nothing. And the ApplicationSet ignores `.spec.syncPolicy`, so an operator may pause one Application by hand and the generator does not undo it. The consequence is that a change of the template's `syncPolicy` does not reach an existing Application; delete the Application (`preserveResourcesOnDeletion` keeps its objects) to make it follow.

## rehearsal-cuts

A rehearsal only, read only, at the end of `core-healthy`. A rehearsal cut is a kustomize overlay `core/rehearsal/<dir>` that removes one way the rehearsal could act on the live outside world. This stage shows from the running cluster that each such link is gone, and stops the rehearsal while one remains; `kluster up` and a rehearsal `seed` both run it, before the database. It reads these, and each must hold:

| Cut | Holds when |
|---|---|
| `cert-manager` | the ClusterIssuer `letsencrypt-prod` has no `acme`, and the Secret `cert-manager/cloudflare-dns-token` does not exist |
| `argocd` | the Deployment `argocd-notifications-controller` has 0 replicas, the Secret `argocd-notifications-secret` does not exist, and the ApplicationSet `projects` does not exist |
| `cnpg` | the ObjectStore `tosak-pg-backups` archives to the rehearsal bucket |
| `monitoring` | the Secret `monitoring/alertmanager-receivers` does not exist, the Alertmanager `kps-alertmanager` mounts no `alertmanager-receivers`, and no Probe exists in `monitoring` |

The table lives in code next to the cuts (`tools/kluster/internal/seed/cuts.go`), and a test fails when a directory in `core/rehearsal` has no entry. `core/rehearsal/reloader` has an entry with a reason and no link: it only drops Roles in namespaces a rehearsal does not make. A broken link is printed as `core/rehearsal/<dir>: <what>`. Fix the cut in git, push the branch `rehearsal` and run again; `kluster down` clears the rehearsal away. Proof: `up --db fresh` on 2026-10-25 reported that all six links were cut. The negative case, a stage that fails and names every link, is a unit test and was not run against a real cluster.

## monitoring-clean

A rehearsal only, read only. `kluster up` runs it after the database and before the zone, because two CNPG rules use `absent()` and fire while no database Cluster exists; a rehearsal `seed` alone does not run it. It is the health test of a rebuilt cluster ([ADR 0011](../adr/0011-monitoring-runs-in-the-cluster-and-a-heartbeat-outside-watches-it.md)): it reads Prometheus through the API server's service proxy (`kubectl get --raw /api/v1/namespaces/monitoring/services/kps-prometheus:http-web/proxy/api/v1/targets` and `…/alerts`), so it needs no port-forward, and waits until three things hold at once:

- every active scrape target is `up`, and at least one exists;
- the Watchdog fires, which proves the rules are evaluated;
- no alert of severity `warning` or `critical` is firing or pending.

Info alerts do not count: they go to the `null` receiver and page nobody (decided 2026-11-01). Pending alerts count, so a fault that has not yet passed its `for` still stops the run; a transient one from the start-up clears while the stage waits. At the timeout (`--timeout`, 30 minutes) it fails and names each target that is not up, with its last scrape error, and each alert with its severity, state and namespace. Whether the dashboards show data stays a check by eye in Grafana; every target up already proves that data comes in.

The rehearsal cut `core/rehearsal/monitoring` sends every alert to `null`, so the stage reads alerts from Prometheus, not from a receiver. Proof: unit tests with recorded API answers (clean, every kind of problem, Prometheus not yet readable), and the rehearsal of 2026-11-01, where it held the run for 30 minutes on two real Grafana faults (sidecars OOMKilled, Grafana killed by its liveness probe) and passed with 37 targets up once both were fixed.

## lb-address

Waits for the Gateway `tosak` to report its address and takes the one public IPv4 (its `status.addresses` also lists an IPv6 and a private address). On live this was `77.42.14.48`.

## zone

Reads the Cloudflare zone `tosak.net` with the read-only token `kluster/cloudflare-zone-read` (Zone:Read and DNS:Read; a POST to `dns_records` gives HTTP 403, proven 2026-10-24) and compares the A records of the hosts the synced routes use. A record that points elsewhere makes the stage refuse success and print `<host>: A <current>; change it to <address>`. **The stage never writes DNS**; writing is a deferred decision.

In `kluster up` the stage runs last, after the database. On live it refuses with the hand changes, and a rerun of `up` resumes. A rehearsal proves the opposite: the zone must still be as live left it. The stage is done when each host has exactly one A record, all hosts hold the same address, and none holds the rehearsal load balancer's address; otherwise it names the host and fails. It cannot compare with the live address, because a rehearsal does not know it and the origin behind Cloudflare stays out of the public repository. In a rehearsal only the hosts of core routes are in scope (`argocd`, `grafana`, the apex and `www` since 2026-11-01), because the cut removes the `projects` ApplicationSet (seen in the rehearsal of 2026-10-25, when only `argocd.tosak.net` was in scope). Before 2026-10-25 a rehearsal refused here by design, with the mismatch report.

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

The rehearsal cuts are kustomize overlays at `core/rehearsal/<dir>` (self-signed ClusterIssuer, notifications off, the `projects` ApplicationSet and the Restore Drill out, no Roles for projects that do not exist, and the CNPG archive store pointed at the rehearsal bucket). Kustomize refuses an overlay inside its own base, so they cannot live in `core/<dir>/rehearsal`. The stage [`rehearsal-cuts`](#rehearsal-cuts) shows from the cluster that they hold. A whole rehearsal, seed included, is `kluster up` ([rehearsal runbook](./rehearsal.md#a-whole-rehearsal-with-kluster-up)).

A `ScheduledBackup` and a `Database` wait for a missing Cluster without error (proven in the rehearsal). `immediate: true` left a Backup in phase `pending` that started as soon as a Cluster appeared, so it is dropped from the ScheduledBackup and `db create` takes the first base backup itself ([recovery runbook](./pg-recovery.md#db-first-backup)).

## Check before a merge

`kluster services check` (and the CI workflow `services-check.yml`) holds the repository to this shape: every synced directory has a `kustomization.yaml` that names no `*.enc.yaml`, no `pg-cluster.yaml`, no `crds.yaml`, no Namespace and no values file, and every project in the generator list has its AppProject, namespace file, `allowedRoutes` entry and `apps.json` rows.
