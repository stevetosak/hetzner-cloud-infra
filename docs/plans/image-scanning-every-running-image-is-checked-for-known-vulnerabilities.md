---
title: "Image scanning: every running image is checked for known vulnerabilities"
description: "Why the cluster should scan every image it runs for known vulnerabilities every day, the choices that come first, and how the result reaches tosak.net as a measured claim."
type: plan
status: proposed
topics: [security, monitoring, ci]
---

Nothing in the cluster or in the app pipeline checks an image for known
vulnerabilities today: a search of `core/`, `projects/`, `.github/` and the
Decision pages on 2026-11-14 found no scanner. Each release deploys a fixed
`alpha-<sha>` tag, so an image with a known critical flaw keeps running until
someone notices.

The need came up in the landing-page grill of 2026-11-14
([the landing-page Plan](landing-page-tosak-net-shows-the-platform-it-runs-on.md)).
tosak.net is to show measured guarantees rather than counts, and "every running
image is scanned every day, with no known critical vulnerability left
unfixed" is one an engineer checks first. The page can state it only once the
scan exists and its result is measured, so the work is a platform feature
first and a number on the page second.

## Steps

**Done when:** every image that runs in the cluster, the platform's own and
the third-party ones, is scanned at least once a day; the results are in
Prometheus with an alert on a new critical finding that has a fix; the app
pipeline reports the findings of each image it builds; and tosak-status serves
the count that the page states, with the date of the last scan.

- [ ] Decide in a grill the scanner (the Trivy Operator in the cluster, a scan
      in CI, or both), which images it covers, and which findings count: every
      critical one, or only those with a fix available, since third-party
      images often carry findings no update can remove yet.
- [ ] Measure the scanner's memory and CPU in the rehearsal project before it
      reaches the live cluster, and size it by ADR 0013; node memory is the
      ceiling of the cluster's own workloads (ADR 0014).
- [ ] The scanner as a core Application, its results in Prometheus, and an
      alert on a new critical finding with a fix.
- [ ] A scan step in the app pipeline for the images the platform builds.
- [ ] tosak-status reads the count and the time of the last scan, and the
      landing page states it.

## Open questions

- Whether the pipeline fails a build on a critical finding with a fix, or only
  reports it. A failing gate blocks every release behind one upstream package.
- Where the vulnerability database comes from, and whether the scanner needs
  an outbound path it does not have today.
