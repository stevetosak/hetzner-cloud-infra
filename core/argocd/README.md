# ArgoCD

`argocd.tosak.net`. ArgoCD **v3.5.3**, chart **10.9.2**, rendered from
`oci://ghcr.io/argoproj/argo-helm/argo-cd`.

| File | What it is |
|---|---|
| `namespace.yaml` | the `argocd` namespace; nothing else declares it |
| `argocd-values.yaml` | every value overridden, each with its reason |
| `render.sh` | pulls the pinned chart to a temp dir and renders |
| `argocd.yaml` | **generated. Do not edit.** Header carries version and digest |
| `credentials.yaml` | the one Secret nothing creates: `argocd-notifications-secret` |
| `httproute.yaml` | the public route, two rules |
| `appprojects.yaml` | one AppProject per deployed project, each scoped to its own namespace |
| `applicationset.yaml` | **the one ApplicationSet** (ADR 0003). One overlay directory is one Application |

## Why this is a render and not an upstream manifest

The rule from step 9 of `docs/runbook/cluster-services.md`: render from a chart
only where values are actually set. ADR 0008 needs two things of ArgoCD that
are plain chart values, so rendering keeps them out of a generated file where
a hand edit would be lost on the next upgrade:

- `--insecure` on `argocd-server`, because TLS terminates at the Gateway.
- a Service port that advertises `appProtocol: kubernetes.io/h2c`, so the
  `argocd` CLI's gRPC survives that termination.

The render is deterministic: two runs of `render.sh` produce identical files.
No template in this chart calls `randAlphaNum`, `genCA` or `uuidv4`. One value
would break that — see the warning at the top of `argocd-values.yaml`.

## Apply

The namespace and the CRDs first, then everything else.

    kubectl apply -f core/argocd/namespace.yaml
    kubectl apply --server-side -f core/argocd/argocd.yaml

    # The operator creates this one — see credentials.yaml.
    kubectl create secret generic argocd-notifications-secret \
      --namespace argocd --from-literal=github-token=<FINE_GRAINED_PAT>

    kubectl apply -f core/argocd/httproute.yaml

    # The declaration of everything ArgoCD deploys. Namespaces first: the
    # AppProjects deny cluster-scoped resources, so ArgoCD cannot make them.
    kubectl apply -f projects/authos/namespace.yaml -f projects/doma/namespace.yaml
    kubectl apply -f core/argocd/appprojects.yaml
    kubectl apply -f core/argocd/applicationset.yaml

🔴 **`--server-side` is mandatory, not a preference.** The
`applicationsets.argoproj.io` CRD alone is about 23,000 lines, far over the
256 KiB limit on the `last-applied-configuration` annotation that a
client-side apply writes. Same constraint as the CloudNativePG operator.

First login:

    kubectl -n argocd get secret argocd-initial-admin-secret \
      -o go-template='{{index .data "password" | base64decode}}'

## One ApplicationSet, and the rule that makes it total

ADR 0003: hand-made Applications are replaced by one ApplicationSet with a git
directory generator. Five of the eight Applications on the lost cluster existed
only in the web UI, and the three that were committed disagreed with each other
about which AppProject they belonged to.

The rule has no exceptions and nothing is mapped by hand:

| Path | `projects/<project>/<component>/manifests/overlays/dev` |
|---|---|
| Application name | `<project>-<component>` |
| AppProject | `<project>` |
| Namespace | `<project>` |

So the Applications are `authos-api`, `authos-demo`, `authos-duster`,
`authos-ui` and `doma-web`. Two of those are renames: the lost cluster called
them `duster` and `doma`. `deployments/apps.json` is keyed on
`.app.metadata.name` and was renamed to match, which cost nothing only because
`deployments/history.jsonl` was still empty. **Anything that keys on an ArgoCD
Application name must be checked whenever this rule changes.**

🔴 **The generator lists its projects, it does not sweep them.** ADR 0003 scopes
the restore to `authos` and `doma`. A `projects/*/*/manifests/overlays/dev`
glob would also adopt `imaps` and `wasteio`, which stay in the repository
unsynced — the exact hazard the ADR says makes this migration cheap against an
empty cluster and expensive against a running one.

### Adding a project

Four edits, all deliberate:

1. one `directories` entry in `applicationset.yaml`;
2. one AppProject in `appprojects.yaml`;
3. `projects/<project>/namespace.yaml`, applied out of band;
4. the namespace added to `allowedRoutes` on **both** listeners in
   `core/gateway/gateway.yaml`, or the project's route is refused with
   `NotAllowedByListeners`.

### What the AppProject actually stops

Each project permits this repository only, its own namespace only, and **no
cluster-scoped resource at all** (`clusterResourceWhitelist: []`). That last
one is why `CreateNamespace=true` is not used: a namespace is cluster-scoped,
so allowing ArgoCD to create one would reopen the boundary the empty list
closes. The namespace is a committed file instead.

### Deleting the ApplicationSet does not delete the workloads

`syncPolicy.preserveResourcesOnDeletion: true`, so the generated Applications
carry no resources finalizer. This cluster has already been lost once to a
single deletion and no database here has a backup yet. The cost: removing a
directory from the generator orphans that component's Deployment — the
Application goes, the workload stays, and it must be deleted by hand.

## The route: two rules, and why ADR 0008 is wrong about it

`argocd-server` serves the web UI and gRPC on **one** container port and splits
them with cmux. The two matchers are exclusive (`server/server.go`, v3.5.3):

    httpL = tcpm.Match(cmux.HTTP1Fast("PATCH"))
    grpcL = tcpm.MatchWithWriters(
        cmux.HTTP2MatchHeaderFieldSendSettings(
            "content-type", "application/grpc"))

The UI listener matches **HTTP/1.x only**, so an h2c connection that is not
gRPC matches no listener at all and cmux drops it. `appProtocol` is
single-valued per Service port, and the chart cannot set it on port 80 in any
case. ADR 0008 describes one port carrying h2c; that would break every browser
request.

So the Service has three ports, all targeting container port 8080:

| Port | Name | `appProtocol` | Routed |
|---|---|---|---|
| 80 | `http` | none | yes — UI, REST, gRPC-web |
| 8080 | `http2` | `kubernetes.io/h2c` | yes — gRPC only |
| 443 | `https` | none | no |

Port 443 serves cleartext while `server.insecure` is true, which is a lie the
chart always renders and no value removes. Nothing points at it.

**The acceptance test is the one unproven claim in ADR 0008:**

    argocd login argocd.tosak.net        # gRPC
    argocd app list                      # gRPC
    # and the UI in a browser

If gRPC fails, the fallback is TLS passthrough with a `TLSRoute`, which is in
the Gateway API **standard** channel as of v1.6 — so it no longer costs the
experimental channel, as ADR 0008 claims.

## Deploy-live notifications

The whole configuration is in `argocd-values.yaml` under `notifications`, and
the chart renders it into `argocd-notifications-cm`. Before 2026-09-21 it lived
in a tracked ConfigMap applied over the rendered one, which meant the render
held data that was overwritten immediately and re-applying the render reverted
the configuration. One source of truth now.

What it does: emit a GitHub `repository_dispatch` (`event_type: deploy-live`)
when an Application goes Synced and Healthy on an image set it has not reported
before. `.github/workflows/deploy-catalog.yml` consumes it.

Two behaviours worth knowing, both measured on 2026-09-21. The controller
**does not crash-loop while its Secret is absent** — it starts, logs
`Controller is running.` as a warning, and waits. And it **watches** the
Secret, so a changed token is picked up with no restart. The old
`notifications/README.md` told the operator to `rollout restart` the
controller; that is not needed.

🔴 **But `invalidated cache for resource … argocd-notifications-secret` is NOT
proof that it saw your change.** The controller emits that exact line on a
**3-minute resync**, at `:03` seconds, as a pair — once for
`argocd-notifications-cm` and once for the Secret. A real change shows up as a
**single line naming one resource, off that 3-minute grid**. Measured on
2026-09-21 while replacing the token: the cadence ran `20:15:03` then
`20:18:03`, and the replacement logged one Secret-only line at `20:17:03`.

This is the step-11 lesson again in a third place: the convenient form of the
check agrees with the design whatever happens. Read the **timestamps and the
pairing**, not the message.

Verify, without sending anything:

    kubectl -n argocd exec deploy/argocd-notifications-controller -- \
      argocd-notifications template notify app-deployed doma-web --recipient gh-infra

    kubectl -n argocd logs deploy/argocd-notifications-controller -f

🔴 **Before trusting it, make a no-op commit on `master` and confirm the
controller logs ONE `on-deployed` fire, not one per Application.** Every
Application tracks the infra repository unscoped, so `sync.revision` advances
for all of them on every commit. `oncePer` keys on `app.status.summary.images`
for exactly this reason. If you see one fire per Application, the `oncePer` key
is wrong and `deploy-catalog` is being hammered.

## What runs, and what it costs

Six workloads: `argocd-application-controller` (a StatefulSet),
`argocd-server`, `argocd-repo-server`, `argocd-applicationset-controller`,
`argocd-notifications-controller`, `argocd-redis`, plus a one-shot
`argocd-redis-secret-init` Job.

Requests total **600m CPU and 1216Mi memory**. The chart sets none; these are
set here because ArgoCD is the control plane for every deploy and a BestEffort
pod is evicted first. Two workers already request about half their memory, and
an unbounded application-controller could push a node into memory pressure and
take CloudNativePG or Redis with it. Neither has a backup yet (Phase 6).

**Dex is disabled.** No SSO is configured. Local accounts are unaffected.

**ArgoCD keeps its own Redis** and does not use the shared one in namespace
`redis`. That one is a 384 MB session store on `volatile-lru` read by Authos
and Duster; ArgoCD's cache is churn and the two would compete for the same
memory, so an ArgoCD eviction would surface as a logged-out user in another
namespace. This one runs `allkeys-lru`, which is right for a pure cache and
would be wrong there. 🔴 It also runs with an explicit `--maxmemory 192mb`
against its 256Mi limit, because the chart sets no `maxmemory` at all and a
memory limit without one is an OOM kill waiting for the cache to fill.

## The five NetworkPolicy objects are inert

The chart renders a NetworkPolicy for each component, and they are kept. **The
cluster runs Flannel, which does not enforce NetworkPolicy at all**, so they
restrict nothing today. They are kept because they cost nothing and would
become correct the day a CNI that enforces them arrives — which ADR 0001 names
as the trigger for Cilium.

Do not read them as a live control. Under Flannel the boundary is what step 11
of `docs/runbook/cluster-services.md` proved for Redis: any pod in the cluster
can reach any other pod's port.
