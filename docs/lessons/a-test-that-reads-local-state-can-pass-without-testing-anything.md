---
title: "A test that reads local state can pass without testing anything"
description: "Why an ArgoCD acceptance test and a local CI simulation both passed without testing what they claimed, and what a check must start from to count as proof."
type: lesson
topics: [gitops, ingress, ci]
evidence: ["../runbook/cluster-services.md"]
---

The acceptance test for ArgoCD behind the new Public Entry Point was
`argocd version` against the public host. It printed the server's version, which
looked like proof that plain gRPC worked end to end. It proved nothing: the CLI
skips its gRPC attempt when its local config already says to use gRPC-web, and
the workstation's config said so, left over from the previous cluster. The
request went out as gRPC-web, with no warning. Run again with an empty config,
plain gRPC failed, and nothing reached Envoy at all — the request was refused
before the origin.

The same fault came back in another form. cloud-infra's docs check was first
proven by a local simulation of its CI job, and it passed. The first run on
GitHub failed (run `37141549597`): `actions/checkout` made a one-commit clone
of `tosak-docs`, the build tried to fetch the full history, and the deploy key
was no longer there to fetch with. The simulation had copied a full clone into
place, so the state the fault depended on never existed locally.

**A check counts as proof only when it runs from a state that cannot already
hold the answer: an empty config, a clean cache, a fresh login, the clone
depth and credentials that CI starts from.**

Found in step 12 of the
[cluster services runbook](../runbook/cluster-services.md#12-argocd--and-the-grpc-claim-adr-0008-could-not-settle),
which also holds the request that isolated the Cloudflare edge from the origin. The CI case is in the
[tosak-docs chunk 4 Log](/tosak-docs/logs/2026-10-03-cloud-infra-pilot-frontmatter-links-and-a-check-that-renders-no-private-page/).
