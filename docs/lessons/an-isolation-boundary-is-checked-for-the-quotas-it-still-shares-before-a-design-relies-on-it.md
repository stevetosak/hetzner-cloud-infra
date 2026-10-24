---
title: "An isolation boundary is checked for the quotas it still shares before a design relies on it"
description: "Why a second Hetzner project did not isolate the rehearsal from live: Primary IPs are counted per account."
type: lesson
topics: [provisioning, kubernetes]
evidence: ["../logs/2026-10-24-services-seed-and-the-core-applicationset-built-rehearsed-and-adopted-on-live.md"]
---

The rehearsal runs in its own Hetzner project, and the design treated that project as isolated from live. Hetzner counts Primary IPs per account, not per project. The rehearsal load balancer was refused with `Primary IP limit exceeded (resource_limit_exceeded)`, the run stalled, and finding the cause took about twenty minutes. While the run was blocked, the account held 9 of its 10 Primary IPs, so live had one address left. A load balancer holds two Primary IPs, one for IPv4 and one for IPv6, which the design had not counted either.

**A separate project, namespace or environment isolates only what its provider scopes to it; check which quotas the boundary still shares with live before a design relies on it.**

Hetzner raised the limit to 15 the same day. The finding and its proof are in the [Log of the services seed task](../logs/2026-10-24-services-seed-and-the-core-applicationset-built-rehearsed-and-adopted-on-live.md).
