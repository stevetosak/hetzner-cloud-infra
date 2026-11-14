---
title: "Applications the cluster hosts"
description: "Each application in projects/, whether the cluster runs it, and its namespace, components, ArgoCD Applications and public hosts."
type: reference
topics: [gitops, kubernetes]
---

The source of truth is the `projects` generator list in
[`core/argocd/applicationset.yaml`](../../core/argocd/applicationset.yaml): an
application runs only when its overlays are listed there
([ADR 0003](../adr/0003-recoverability.md)). This page follows that list. When
the list changes, this page changes in the same pull request.

## Synced applications

| Application | Namespace | Component | ArgoCD Application | Deployment | Public host |
|---|---|---|---|---|---|
| authos | `authos` | api | `authos-api` | `authos-api` | `authos-api.tosak.net` |
| authos | `authos` | ui | `authos-ui` | `authos-ui` | `authos.tosak.net` |
| authos | `authos` | demo | `authos-demo` | `authos-demo` | `authos-demo.tosak.net` |
| authos | `authos` | duster | `authos-duster` | `duster` | `authos-demo.tosak.net/duster` |
| doma | `doma` | web | `doma-web` | `doma` | `doma.tosak.net` |
| tosak-net | `tosak-net` | site | `tosak-net-site` | `tosak-net-site` | `tosak.net`; `www.tosak.net` redirects to it |

The ArgoCD Application name is generated as `<application>-<component>` from
the overlay path; the Deployment keeps the name in its own manifest, so
`duster` and `doma` differ from their Applications. `deployments/apps.json` is
keyed on the Application name.

Checked 2026-10-31 with `kubectl get applications -n argocd` (all five
`Synced`, `Healthy`) and `kubectl get deploy -n authos` and `-n doma` (all five
`1/1`).

tosak-net was added on 2026-11-13 and took the apex on 2026-11-14. Checked
that day: `tosak-net-site` `Synced`, `Healthy` and `1/1` on its first deployed
tag; `curl` gave 200 on `tosak.net` and a 302 to it on `www.tosak.net`, and
`probe_success` was 1 for both. The route `apex-to-docs`, which had sent both
hosts to the docs since 2026-10-25, is gone.

## Applications kept but not synced

| Application | Components in the repository |
|---|---|
| wasteio | api, frontend, mosquitto, simulator, waste-bin-agent |
| imaps | backend, frontend |

These stay in `projects/` with overlays, routes and Database files, but they
are not in the generator list. They have no Application, no namespace and no
applied route, so their hosts (`wasteio.tosak.net`, `imaps.tosak.net` and the
`-api` hosts) serve nothing. Checked 2026-10-31: `kubectl get ns` lists neither
namespace. `projects/debug/` is a bare pod manifest, not an application.

## Reviving or adding an application

Adding an application to the generator is five changes: the line in the
generator list, an `AppProject` in `core/argocd/appprojects.yaml`, a
`projects/<application>/namespace.yaml`, the namespace in `allowedRoutes`
in `core/gateway/gateway.yaml`, and its LimitRange directory under
`core/limit-ranges/`, with the namespace in the `core` AppProject. `kluster
project add <application>` writes all of them, and with `--component` the
deploy catalog row in `deployments/apps.json`; `kluster services check` fails
when one is missing. The component's own manifests, its route and the Probe
target of each host stay hand-written.
