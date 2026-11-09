---
title: "Landing page: tosak.net shows the platform it runs on"
description: "How tosak.net goes from a prototype to a live page on the cluster: the tosak-net repository and its design system, the site in place of the apex redirect, and the tosak-status service that keeps its numbers live."
type: plan
status: active
topics: [ingress, monitoring, ci]
---

Since 2026-10-25 the apex and `www` send every visitor to the docs site with a
302, a placeholder until a landing page owns them
([the redirect's Log](../logs/2026-10-25-the-apex-and-www-redirect-to-the-docs-site-with-a-302.md)).
The page that replaces it is a showcase of the cluster as a platform and of
the person who runs it. It offers no services yet. Its design was chosen
after two rounds of prototypes: the "Working model", an isometric scale model
of the cluster drawn as a small factory. A loop keeps the factory in step with
Git, and six chapters break it and watch it recover: a release, a failed job,
a stopped machine, lost data, a rebuild from nothing. Version 11 of that
prototype was accepted on 2026-11-09. Its design is also the baseline for
later tosak applications, which are React (Authos UI, doma), so the design
system is built as React components that the page consumes rather than as
styles inside the page.

The prototype is one HTML file built by a Python generator, outside any
repository. Its proof numbers (checks, releases, the drill) were measured by
hand on 2026-11-08 and baked in with that date. The live numbers come from a
small service, tosak-status, which reads Prometheus and the deploy catalog and
returns one cached JSON of rounded values. Prometheus itself is never
exposed.

The build shape was settled with the operator on 2026-11-09. One new public
repository, `stevetosak/tosak-net`, holds a pnpm workspace: `packages/design`
(tokens, React primitives, the isometric model kit), `apps/site` and
`services/status` (Go). Astro renders every component to static HTML at build
time, and only the hero factory and the tour camera hydrate as React islands.
The animation stays on the browser's Web Animations API, with the prototype's
clock ported as a typed module rather than replaced by a library. The site
goes live first with dated build-time numbers, and tosak-status follows.

## The repository and the design system

**Done when:** `stevetosak/tosak-net` is public and its CI gate (lint,
typecheck, test, build) passes on `main`; `packages/design` holds the tokens,
the primitives the page uses and the model kit, with unit tests for the
projection, the clock and the reconcile loop; a Decision page in the
repository records the build shape and publishes on docs.tosak.net.

- [ ] Create the repository: pnpm workspace, strict TypeScript, ESLint,
      Prettier, Vitest, and the CI gate on every push. Commit the built v11
      page and its generator under `reference/` as the visual baseline, without
      the working brief.
- [ ] Add the repository to the tosak-docs sources and write its first
      Decision page: one repository, Astro with React islands, native Web
      Animations, the site before live numbers, and the design package on npm
      only once a second application uses it.
- [ ] Tokens: one TypeScript source for the prototype's colour (OKLCH), type
      scale, space, motion and rule values, emitted as CSS custom properties.
      Rethink Sans and Spline Sans Mono are self-hosted through Fontsource, so
      the page makes no Google Fonts request.
- [ ] Primitives: the components the page repeats (container and 12-column
      grid, section heading, lead, button, text link, fact, readout, chapter
      button), each with the prototype's hover, focus and disabled states.
- [ ] Model kit: the generator ported to TypeScript (the isometric
      projection, box, slab and lamp as SVG components, and one scene
      description of the plant shared by the hero and the tour). A test
      compares projected points with the Python generator's output for the
      same scene.
- [ ] Motion: the hero clock (one pausable time base, timers on clock time,
      tracked animations, abort on a chapter jump) and the reconcile loop as
      framework-free modules. Tests on a fake clock prove that a release
      starts one new job and retires an old one only when the new one is
      ready, that a stopped machine's jobs start again elsewhere, and that
      Pause holds every timer.

## The site on the cluster

**Done when:** `https://tosak.net` returns the landing page from the cluster
as static HTML with its build-time numbers and their date; `www.tosak.net`
redirects to it; the apex redirect is gone from Git and from the live
Gateway; the public-hosts Probe expects a 200 from the apex and reports
success; and the page passes its browser tests at 1440 px and 375 px.

- [ ] Build the page in `apps/site` from the design primitives. Reduced
      motion shows the plant at rest with the chapter text, and the page reads
      completely with JavaScript off.
- [ ] Put the numbers in one build-time JSON with its measured time, in the
      shape tosak-status will return. Correct "Checked from outside every
      minute": the probes run inside the cluster and reach each host through
      its public address, out through Cloudflare and back.
- [ ] Browser tests in CI with Playwright: every chapter runs without a
      script error, Pause holds the caption and the timer, the tour camera
      follows the steps, and an axe scan finds no violation.
- [ ] Image `stevetosak/tosak-net-site`: an unprivileged static server pinned
      by digest, built and tagged by the platform's app pipeline (read the
      `docker` and `app-ci` rules first).
- [ ] The project in this repository: `projects/tosak-net/` with the site
      component, its route for `tosak.net` and `www.tosak.net` (`www`
      redirects to the apex with a 302 until the page is stable), and every
      entry `kluster services check` asks for. Requests start from the guess
      and follow ADR 0013 after three days.
- [ ] Cut over. The new route is applied while `apex-to-docs` still exists;
      under the Gateway API's tie rule the older route keeps the host, so
      nothing changes yet (assumed until the first apply shows it). Deleting
      `apex-to-docs` by hand then hands the host to the site at once. 🔴
      `core-gateway` syncs with prune off, so removing
      `core/gateway/apex-redirect.yaml` from Git alone leaves the live route
      in place. The same PR moves the apex from `public-hosts-302` to
      `public-hosts-2xx`. Proof is taken on the live objects: `curl` for the
      apex and `www`, and `probe_success` for both targets.

## Live numbers from tosak-status

**Done when:** tosak-status runs in the cluster and answers
`https://tosak.net/api/status` with one JSON of rounded values, cached for
about 60 seconds; no Prometheus endpoint is reachable from a public host; the
page shows the live values with their measured time and falls back to the
build-time values when the service fails; and every value matches a manual
query taken at the same time.

- [ ] Decide in a grill the source and window of each number: checks and
      failures (the public-hosts Probe), applications that match Git (Argo CD's
      application metrics), releases (the deploy catalog), recorded decisions
      (the Decision pages), the last restore drill (the `restore-drill` Job),
      and the per-host share of passed checks. Each is counted either since a
      fixed date or over a rolling window.
- [ ] The contract: one JSON Schema in the repository. The Go service's tests
      validate its response against the schema, and the site's types are
      generated from it.
- [ ] The service in `services/status`: Go, the standard library's HTTP
      server and Prometheus' Go API client. It refreshes one snapshot about
      every 60 seconds, serves the last good one with its time, and sets a
      matching `Cache-Control`. `/health/live` and `/health` follow the
      platform convention. Any credential it needs is a Secret Catalog entry.
- [ ] Image `stevetosak/tosak-net-status` as the second component of
      `projects/tosak-net/`, and an `/api/status` rule on the `tosak.net`
      route.
- [ ] The page fetches `/api/status` after load and swaps in the values and
      the measured time; on an error or a stale snapshot it keeps the
      build-time values and their date.

## Open questions

- Dark mode. The prototype is light only. The page can ship light only, but a
  design system that later applications build on may need a dark token set
  from the start.
- Old links under the apex. The redirect kept the path, so
  `tosak.net/cloud-infra` lands on the docs site today. After the cutover the
  site answers every path; its 404 page can link to the docs, or a route rule
  can keep sending known docs paths there.
- The npm scope and the trigger for publishing `packages/design`, once a
  second application takes it on.
