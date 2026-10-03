---
title: "A test that reads local state can pass without testing anything"
description: "Why an ArgoCD acceptance test reported plain gRPC working through the public host when it had never tried it, and what a check must start from to count as proof."
type: lesson
topics: [gitops, ingress]
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

**A check counts as proof only when it runs from a state that cannot already
hold the answer: an empty config, a clean cache, a fresh login.**

Found in step 12 of the
[cluster services runbook](../runbook/cluster-services.md#12-argocd--and-the-grpc-claim-adr-0008-could-not-settle),
which also holds the request that isolated the Cloudflare edge from the origin.
