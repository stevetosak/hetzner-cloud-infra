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
styles inside the page. This Plan lays only its base, the tokens, primitives
and model kit the page needs; the design system itself is a body of work of
its own, with
[its own Plan](https://docs.tosak.net/tosak-net/plans/design-system-a-package-the-tosak-applications-share/).

The prototype is one HTML file built by a Python generator, outside any
repository. Its proof numbers (checks, releases, the drill) were measured by
hand on 2026-11-08 and baked in with that date. The live numbers come from a
small service, tosak-status, which reads Prometheus, its own daily history
and the restore drill's results, and returns one cached JSON of rounded
values. Prometheus itself is never exposed.

The build shape was settled with the operator on 2026-11-09. One new public
repository, `stevetosak/tosak-net`, holds a pnpm workspace: `packages/design`
(tokens, React primitives, the isometric model kit), `apps/site` and
`services/status` (Go). Astro renders every component to static HTML at build
time, and only the hero factory and the tour camera hydrate as React islands.
The animation stays on the browser's Web Animations API, with the prototype's
clock ported as a typed module rather than replaced by a library. The site
goes live first with dated build-time numbers, and tosak-status follows.

## The repository and the base of the design system

**Done when:** `stevetosak/tosak-net` is public and its CI gate (lint,
typecheck, test, build) passes on `main`; `packages/design` holds the base of
the design system (the tokens, the primitives the page uses and the model
kit), with unit tests for the projection, the clock and the reconcile loop; a
Decision page in the repository records the build shape and publishes on
docs.tosak.net; and the design system's own Plan publishes there with status
proposed.

- [x] Create the repository: pnpm workspace, strict TypeScript, ESLint,
      Prettier, Vitest, and the CI gate on every push. Commit the built v11
      page and its generator under `reference/` as the visual baseline, without
      the working brief.
- [x] Add the repository to the tosak-docs sources and write its first
      Decision page: one repository, Astro with React islands, native Web
      Animations, the site before live numbers, and the design package on npm
      only once a second application uses it.
- [x] Tokens: one TypeScript source for the prototype's colour (OKLCH), type
      scale, space, motion and rule values, emitted as CSS custom properties.
      Rethink Sans and Spline Sans Mono are self-hosted through Fontsource, so
      the page makes no Google Fonts request.
- [x] Primitives: the components the page repeats (container and 12-column
      grid, section heading, lead, button, text link, fact, readout, chapter
      button), each with the prototype's hover, focus and disabled states.
- [x] Model kit: the generator ported to TypeScript (the isometric
      projection, box, slab and lamp as SVG components, and one scene
      description of the plant shared by the hero and the tour). A test
      compares projected points with the Python generator's output for the
      same scene.
- [x] Motion: the hero clock (one pausable time base, timers on clock time,
      tracked animations, abort on a chapter jump) and the reconcile loop as
      framework-free modules. Tests on a fake clock prove that a release
      starts one new job and retires an old one only when the new one is
      ready, that a stopped machine's jobs start again elsewhere, and that
      Pause holds every timer.
- [x] Write the design system's own Plan in the repository, with status
      proposed: what it adds beyond the page's base, and what a second
      application needs before it takes the package on. The open questions of
      dark mode and of the npm scope and publishing trigger move there.

## The site on the cluster

**Done when:** `https://tosak.net` returns the landing page from the cluster
as static HTML with its build-time numbers and their date; `www.tosak.net`
redirects to it; the apex redirect is gone from Git and from the live
Gateway; the public-hosts Probe expects a 200 from the apex and reports
success; and the page passes its browser tests at 1440 px and 375 px.

- [x] Build the page in `apps/site` from the design primitives. Reduced
      motion shows the plant at rest with the chapter text, and the page reads
      completely with JavaScript off.
- [x] Put the numbers in one build-time JSON with its measured time, in the
      shape tosak-status will return. Correct "Checked from outside every
      minute": the probes run inside the cluster and reach each host through
      its public address, out through Cloudflare and back.
- [x] Browser tests in CI with Playwright: every chapter runs without a
      script error, Pause holds the caption and the timer, the tour camera
      follows the steps, and an axe scan finds no violation.
- [x] Image `stevetosak/tosak-net-site`: an unprivileged static server pinned
      by digest, built and tagged by the platform's app pipeline (read the
      `docker` and `app-ci` rules first).
- [x] The project in this repository: `projects/tosak-net/` with the site
      component, its route for `tosak.net` and `www.tosak.net` (`www`
      redirects to the apex with a 302 until the page is stable), and every
      entry `kluster services check` asks for. Requests start from the guess
      and follow ADR 0013 after three days.
- [x] Cut over. The new route is applied while `apex-to-docs` still exists;
      under the Gateway API's tie rule the older route keeps the host, so
      nothing changes yet (shown on 2026-11-14: both routes Accepted with
      `RouteRulesOverlap`, and both hosts still answered 302 to the docs). Deleting
      `apex-to-docs` by hand then hands the host to the site at once. 🔴
      `core-gateway` syncs with prune off, so removing
      `core/gateway/apex-redirect.yaml` from Git alone leaves the live route
      in place. The same PR moves the apex from `public-hosts-302` to
      `public-hosts-2xx`. Proof is taken on the live objects: `curl` for the
      apex and `www`, and `probe_success` for both targets. Done 2026-11-14
      (#118): a delete made before `core-gateway` had read the merge was
      undone by selfHeal within seconds; the second delete, after the sync,
      held. The apex gave 200, `www` a 302 to it, `probe_success` 1 for both.

## Live numbers from tosak-status

The grill of 2026-11-14 changed what the page counts. Counts of volume
(checks, releases, applications in sync, recorded decisions) read as small for
a personal platform and say nothing about whether it can be trusted. What an
engineer trusts is a measured guarantee, so every number the page keeps is
one: how available each public host was, how far behind the offsite copy of
the data can fall, how long a verified restore takes, and how long a rebuild
from nothing takes. The GitOps and records chapters keep their text and lose
their numbers.

A guarantee needs a long window, and Prometheus keeps three days on an
emptyDir that a pod restart erases (`retention: 3d` in
`core/monitoring/kube-prometheus-stack-values.yaml`; on 2026-11-14 the oldest
sample was 2026-11-11 06:00 UTC). tosak-status therefore keeps its own
history: one row per probe target and UTC day in its own database on the
shared PostgreSQL cluster, written once the day closes and filled at start-up
for every missing day Prometheus still holds. An outage of the service shorter
than three days loses nothing; a longer gap stays a gap, and the page counts
only the days it has. The history is backed up and drilled with every other
database, the mechanism the page itself advertises.

**Done when:** tosak-status runs in the cluster and answers
`https://tosak.net/api/status` with one JSON of rounded values, cached for
about 60 seconds; no Prometheus endpoint is reachable from a public host; the
page shows the live values with their measured time and falls back to the
build-time values when the service fails; and every value matches a manual
query taken at the same time.

- [x] Decide in a grill the source and window of each number. Decided
      2026-11-14:

      | Number | Source | Window |
      |---|---|---|
      | Checks, failed checks and availability per host | `probe_success` of the `public-hosts` job, all its targets; the page picks the hosts it shows | 30 rolling days of the daily history; no stated target |
      | How far behind the offsite copy fell | `cnpg_pg_stat_archiver_seconds_since_last_archival` (its daily max) and the failed archivals | 30 rolling days; `archive_timeout` is 300 s, so the worst case is about 5 min |
      | The restore drill: restore time, rows checked, result, date | the newest result JSON in R2 `tosak-drill-results`, read with a read-only token for that bucket | the last run; the next is the 1st of the month |
      | The rebuild from an empty project | the rehearsal Log of 2026-10-25: `up --db fresh --apply` in about 18 min | build-time, updated by hand after each rehearsal |

      The rebuild chapter states what was measured: a fresh database, with
      the restore proven by the monthly drill. A rebuild timed with
      `--db recover` from empty does not exist yet.
- [x] The contract: one JSON Schema in the repository. The Go service's tests
      validate its response against the schema, and the site's types are
      generated from it. Done 2026-11-14 (tosak-net #10):
      `packages/status-contract` holds the schema, with three sections
      (availability, offsite copy, drill) that each carry their own
      `measuredAt`, so the page falls back one section at a time. The site's
      types are generated with json-schema-to-typescript and a test fails when
      they drift; the build-time snapshot is validated with Ajv. The page went
      live the same day with the new numbers, measured over the three days
      Prometheus held: 99.98 % of 34,561 checks passed, a restore of 3 min 17 s
      in the drill of 1 November, and an offsite copy never more than 5 min 1 s
      behind.
- [ ] The service in `services/status`: Go, the standard library's HTTP
      server and Prometheus' Go API client. It refreshes one snapshot about
      every 60 seconds, serves the last good one with its time, and sets a
      matching `Cache-Control`. It writes the daily history to the
      `tosak_status` database and fills missing days at start-up.
      `/health/live` and `/health` follow the platform convention. Its
      credentials (the database role, the read-only R2 token for
      `tosak-drill-results`) are Secret Catalog entries.
- [ ] Image `stevetosak/tosak-net-status` as the second component of
      `projects/tosak-net/`, and an `/api/status` rule on the `tosak.net`
      route.
- [ ] The page fetches `/api/status` after load and swaps in the values and
      the measured time; on an error or a stale snapshot it keeps the
      build-time values and their date.

## Open questions

- Old links under the apex. The redirect kept the path, so
  `tosak.net/cloud-infra` lands on the docs site today. After the cutover the
  site answers every path; its 404 page can link to the docs, or a route rule
  can keep sending known docs paths there.
