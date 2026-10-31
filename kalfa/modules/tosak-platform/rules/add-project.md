---
paths:
  - "core/**"
  - "projects/**"
  - "secrets.yaml"
  - "deployments/**"
---
# cloud-infra: the layout, Argo CD, and adding a project

```
core/                              # cluster-wide, not per project
  cnpg/pg-cluster.yaml             # the CloudNativePG cluster
  cnpg/databases/<project>.yaml    # one Database per project database
  redis/  cert-manager/  gateway-api/  gateway/
  argocd/                          # Argo CD itself, a pinned helm render; synced by core-argocd
    argocd-values.yaml             # --insecure, the h2c service port, on-deployed notifications
    httproute.yaml                 # gRPC to the h2c port, everything else to 80
    applicationset.yaml            # `projects`: one Application per listed overlay
    core-applicationset.yaml       # `core`: one Application core-<dir> per synced core dir
    appprojects.yaml               # the `core` AppProject and one AppProject per project
  <dir>/crds.yaml, namespace.yaml  # applied by `kluster services seed`, never by Argo CD
  rehearsal/<dir>/                 # rehearsal cuts (kustomize overlays) of core dirs
projects/<project>/                # namespace.yaml, httproute.yaml, one folder per component
deployments/                       # apps.json, resolve.mjs, record.mjs, render-catalog.mjs
secrets.yaml                       # the Secret Catalog
.github/workflows/                 # deploy-catalog.yml, deploy-scope.yml
.github/actions/tag-bump/          # the action app repositories call
```

`history.jsonl` and `CATALOG.md` live on the orphan branch `deploy-catalog`, not on `master`.

## What Argo CD syncs, and what is applied out of band

Argo CD syncs every listed project overlay (`projects` ApplicationSet) and every core directory in `core/argocd/core-applicationset.yaml` (argocd, cert-manager, cnpg, gateway, pg-drill, redis, reloader) as `core-<dir>`, selfHeal on, prune off. A hand change to a core object is reverted to git: change `core/` through a commit to `master`.

`kluster services seed` (`tools/kluster`, runbook `docs/runbook/services-seed.md`) applies what Argo CD cannot or must not own: every `core/*/crds.yaml`, every namespace file, the Secrets of the Secret Catalog, and the first Argo CD install. It runs in Plan Mode without `--apply`.

Applied by hand: each component's `configmap.yaml` and `service.yaml`; `projects/<project>/httproute.yaml`; the `*.enc.yaml` Secrets (`kluster secrets apply`, or the seed); `core/cnpg/pg-cluster.yaml` and `core/cnpg/recovery/` (the database Cluster stays out of Argo CD on purpose). `core/hcloud` and `core/cni` come from `kluster cp init`.

Nothing creates a namespace in a sync. Every project AppProject sets `clusterResourceWhitelist: []`, so `CreateNamespace=true` is used nowhere and would reopen that boundary.

## The generator rule

Never write an `argocd-application.yaml`. The `projects` ApplicationSet maps every overlay by one rule:

```
projects/<project>/<component>/manifests/overlays/dev
  Application <project>-<component>   AppProject <project>   namespace <project>
```

The generator LISTS its projects (`- path: projects/<project>/*/manifests/overlays/dev`); a `projects/*/*` glob would adopt the projects kept unsynced (`imaps`, `wasteio`, `projects/debug/`). That list is the source of truth for which projects are live; `docs/reference/applications-the-cluster-hosts.md` in cloud-infra lists them with their namespaces, Applications and hosts.

Deleting the ApplicationSet does not delete the workloads (`preserveResourcesOnDeletion: true`). Removing a directory from the list orphans that component's Deployment: still running, no longer managed.

## Adding a project

1. **Four entries:** one line in the generator list, one `AppProject` in `core/argocd/appprojects.yaml`, one `projects/<project>/namespace.yaml`, and the namespace in `allowedRoutes` in `core/gateway/gateway.yaml`. A Deployment with the Reloader annotation also needs its namespace in Reloader's `--namespaces`, a Role and RoleBinding in `core/reloader/reloader.yaml`, and the `core` AppProject destinations. `kluster services check` (CI `services-check.yml`) fails when one is missing.
2. **The component files** (see the `manifests` rule).
3. **A database**, if needed: `core/cnpg/databases/<project>.yaml`, listed in `core/cnpg/kustomization.yaml`; `core-cnpg` syncs it.

   ```yaml
   apiVersion: postgresql.cnpg.io/v1
   kind: Database
   metadata:
     name: <project>-db
     namespace: pg-cluster
   spec:
     name: <project>
     owner: tosak
     cluster:
       name: tosak-pg-cluster
   ```

   The owner role is `tosak`, which owns every project's database. Connection: `postgresql://tosak:<password>@tosak-pg-cluster-rw.pg-cluster.svc.cluster.local:5432/<project>`; the password is in the Secret `db-credentials` in `pg-cluster`.
4. **Secrets**, committed encrypted with SOPS and age. No `init_secrets.sh` (retired). Each Secret is an entry in the Secret Catalog `secrets.yaml`: its `<namespace>/<name>`, its file `manifests/<name>.enc.yaml`, and where each key comes from (`generated`, `issued` from a Bitwarden item `kluster/*`, `copy` of another key, or `literal`). Then, from `tools/kluster`:

   ```bash
   go run . secrets generate --apply          # missing generated values and every copy
   go run . secrets pull --apply              # issued values from Bitwarden
   go run . secrets apply --apply <namespace>/<name>
   ```

   Every write runs in Plan Mode without `--apply`, and no command prints a value. The commands that decrypt read the age key from Bitwarden, so the Operator runs them in their own terminal. Only `data` is encrypted; the repository is public.
5. **The deploy catalog:** an entry in `deployments/apps.json`, keyed on the generated Application name:

   ```json
   "<project>-<component>": { "repo": "stevetosak/<repo>", "tagPrefix": "<prefix>-v", "host": "<project>.tosak.net" }
   ```

   The key is `.app.metadata.name` from the notification: if the naming rule changes, this file changes with it, or the catalog records `version: "unknown"` and `CATALOG.md` grows a second row that never goes away. `"repo": null` when the app has no release-please or semver tags yet.

## The first deploy, in order

1. Merge the infra PR with the project's files (overlay, namespace file, AppProject, generator line, `allowedRoutes`, `apps.json`, Secret Catalog entry, database); `kluster services check` passes. `core-argocd` and `core-cnpg` sync their parts.
2. Create the namespace and the Secrets: `go run . services seed --env live` (Plan Mode), then `--apply`; or one Secret with `go run . secrets apply --apply <namespace>/<name>`.
3. `kubectl apply -f manifests/configmap.yaml -f manifests/service.yaml`
4. `kubectl apply -f projects/<project>/httproute.yaml`
5. Trigger the app repository's CI, so its deploy job bumps the overlay tag.

From then on every deploy is automatic.
