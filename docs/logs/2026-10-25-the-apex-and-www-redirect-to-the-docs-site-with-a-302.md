---
title: "The apex and www redirect to the docs site with a 302"
description: "Why the apex and www redirect to docs.tosak.net with a 302 from a Gateway route in git, and what the redirect changed in the zone check."
type: log
date: 2026-10-25
---

The Plan item "decide what `tosak.net` and `www.tosak.net` should serve" is done. Both names reached the Gateway and matched no route, so Envoy answered 404. The operator decided on 2026-10-25 that both redirect to `docs.tosak.net`. The redirect is a placeholder: a landing page or hub at the apex is intended later, as a separate task that is not planned yet.

The redirect is a Gateway `HTTPRoute` in git, not a Cloudflare Redirect Rule in the dashboard. A route in git is reviewed, synced by ArgoCD and reverted when changed by hand. The status code is 302, because a browser caches a 301 with no end date and the later landing page would then be hidden for visitors who had followed the redirect once.

PR #62 (merge 80bc09a) added `core/gateway/apex-redirect.yaml`: HTTPRoute `apex-to-docs` in namespace `gateway`, `sectionName: https`, hostnames `tosak.net` and `www.tosak.net`, a `RequestRedirect` to scheme https and hostname `docs.tosak.net` with status 302. The path is kept. The file is listed in `core/gateway/kustomization.yaml` and `core-gateway` syncs it. No `allowedRoutes` change was needed, because `gateway` was already listed on both listeners. The seed test in `tools/kluster/internal/seed/seed_test.go` now expects the two new hosts.

After the sync the route showed `Accepted=True` and `ResolvedRefs=True`. Requests proved the behaviour: `https://tosak.net/` answered 302 to `https://docs.tosak.net/`, and `https://www.tosak.net/cloud-infra` answered 302 to `https://docs.tosak.net/cloud-infra`. `http://tosak.net/` answered 301 to https from the existing `http-to-https` route, then the 302. doma and ArgoCD still answered 200. The redirect is answered by Envoy, behind the origin lock, so the apex and `www` pass through Authenticated Origin Pulls like every other host.

## 🔴 A new route hostname changes what the seed checks in the zone

The `zone` stage of `kluster services seed` takes its hosts from the hostnames of every synced route. It now also checks that the apex and `www` A records point at the load balancer (live), or still at live (rehearsal). Passing on live is **assumed**: both names reached the Gateway before. The next live `services seed` is the real test.

## The path is kept, and the docs site has few top-level pages

The docs site has no page at `/cloud-infra` (404, also with the trailing slash); page paths such as `/cloud-infra/context/` exist. Because the redirect keeps the path, only full page paths land on a page. This belongs to the docs site, not to the route.
