---
title: "Providers: the platform adapts to more than one"
description: "Why tosak.net should stop depending on Hetzner alone, which parts of the cluster are bound to one provider today, and the design questions that come before any change."
type: plan
status: proposed
topics: [provisioning, kubernetes, storage, networking]
---

Every node of the cluster runs on Hetzner Cloud, and several parts of the
platform assume it. The cost of that showed on 2026-11-08: the operator found
that `cx23`, the default `server_type` of the control plane
(`infra/control-plane/variables.tf`) and the type the workers were sized on,
can no longer be ordered, so the cluster cannot grow at the price it was
planned for. A single provider can change its offer at any time, and today the
only answer is to accept what that provider sells.

The direction is a platform that adapts to the provider, not one bound to any
one. This is a large design task and it waits: the cluster is to reach a stable
state first (the active Plans, monitoring and autoscaling), and the work is
taken up when the operator starts it. The immediate `cx23` gap is not part of
this Plan; another Hetzner type fills it.

The parts bound to one provider today, read from the repository on 2026-11-08:

- **Compute and network** — the Terraform Modules under `infra/` use the
  `hcloud` provider for servers, the private network and the firewall, and
  kluster builds and proves the cluster against a Hetzner project
  ([ADR 0009](../adr/0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md)).
- **Storage** — the default StorageClass `hcloud-volumes` comes from the
  Hetzner CSI driver (`core/hcloud/`), and its volumes are bound to one
  location, so the PostgreSQL workers are too
  ([ADR 0005](../adr/0005-storage-and-worker-identity.md)).
- **Entry point** — the public Gateway sits behind a Hetzner load balancer with
  PROXY protocol, created by the Hetzner cloud controller manager
  ([ADR 0008](../adr/0008-gateway-api-and-the-public-entry-point.md)).
- **Outside the cluster** — Cloudflare holds DNS and the DNS-01 challenge, and
  R2 holds the PostgreSQL backups and the Terraform state. These are separate
  dependencies with the same question.

## Design

**Done when:** a Decision page records what "adapts to a provider" means for
tosak.net, which of the bound parts move behind an interface and which stay
provider-specific, and the order of the work.

- [ ] Decide in a grill what the goal is: a cluster that can be rebuilt on a
      second provider in a bounded time, nodes from more than one provider in
      one cluster, or something between. Each has a different cost.
- [ ] Decide in a grill, for each bound part above, whether it stays, gets a
      provider-neutral replacement, or gets one implementation per provider.
- [ ] Decide which second provider proves the design, and how a rehearsal
      runs against it.

## Open questions

- Storage is the hardest part: a location-bound block volume does not move
  between providers. Whether PostgreSQL leans on its own replication and the
  R2 backups instead is part of the storage decision.
- The name of what tosak.net is. It runs on other providers' compute, so
  "cloud platform" may not fit; the operator has not chosen a name.
