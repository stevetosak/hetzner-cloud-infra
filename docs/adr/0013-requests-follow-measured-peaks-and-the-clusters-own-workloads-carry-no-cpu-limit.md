---
title: "Requests follow measured peaks, and the cluster's own workloads carry no CPU limit"
description: "The sizing rule that sets every container's memory and CPU request from its measured peak, why a container is not sized by a house default, and why only client namespaces get a CPU limit."
type: decision
status: accepted
date: 2026-11-05
topics: [kubernetes, provisioning, monitoring]
---

The scheduler places a pod by what it requests, not by what it uses. Until
this decision, a container got `200m` / `512Mi` requested and `500m` / `1Gi` as
limits unless someone thought harder, and most did not. On 2026-11-05 the pods
on the three workers requested 9594 MiB of memory while the sum of their 3-day
peaks was 4707 MiB, measured by
[`capacity-snapshot.py`](../../core/monitoring/capacity-snapshot.py) against
Prometheus (`max_over_time(container_memory_working_set_bytes[3d])` per
container). The largest gaps were PostgreSQL (153 MiB used per instance
against 1024 Mi requested), Envoy (32 against 512), `authos-ui` (4 against
512) and `doma` (64 against 512). In the other direction, Prometheus used
848 MiB against 400 Mi, and several containers requested nothing at all.

The cost of the gap is a refused workload, not wasted memory. A new pod is
turned away when the requests are full, long before the memory runs out, and
the cluster then appears to need a fourth node it does not need. Workers are
paid for out of pocket, so a purchase has to follow a measurement
([the Capacity Plan](../plans/capacity-right-size-then-grow-by-cost.md)). The
rule below also becomes the default for every client workload the cluster
hosts later, which is what makes it hard to change once set.

## The decision

**A container's memory request is its measured peak working set times 1.3,
rounded up to 16 Mi and never under 32 Mi; its memory limit is twice the
request. Its CPU request is its measured peak 5-minute rate times 1.3, never
under `10m`, and the cluster's own workloads carry no CPU limit.** The peak is
read from Prometheus over at least three days. PostgreSQL keeps at least
384 Mi (128 MB `shared_buffers`, the CloudNativePG default, plus its working
memory) and Envoy at least 128 Mi for traffic bursts.

Three cases do not fit the measurement, and each has its own clause:

- **Startup is checked after the change, not before it.** The first
  measurements contain no startup peak: almost every pod started before
  Prometheus did (2026-11-01 12:06 UTC), so a JVM start or an Argo CD resync
  storm is not in the data. Each resize rolls its pod anyway, so the new pod's
  peak over its first hour is compared with the new request, and the request
  goes up before the next change if the peak is higher.
- **A process that sizes itself from a cap keeps the limit its cap needs.**
  Redis runs with `maxmemory` below its limit (384 MB in 512 Mi for the shared
  instance, 192 MB in 256 Mi for Argo CD's), and a JVM takes its heap as a
  share of the limit. For these only the request follows the rule; the limit
  stays where the cap puts it. A limit reserves nothing, so this costs no
  scheduling room.
- **A periodic workload keeps its values until a measured window holds a
  run.** The monthly restore drill had no run inside the first snapshot
  windows; it is sized after the 2026-12-01 run has been measured.

Client namespaces are different: their LimitRange (`core/limit-ranges`) adds a
CPU limit, because a client's workload is not trusted the way the cluster's own
is.

## Why this and not the alternatives

Keeping a house default was the cheapest option to write and the most
expensive to run. One number for every container is wrong for nearly all of
them, in both directions: it reserved 512 Mi for a static UI that uses 4 MiB,
and left Prometheus and Grafana above their requests, the first to be evicted
under memory pressure.

The Vertical Pod Autoscaler would compute the same numbers continuously, but it
is another controller, its recommender needs history of its own, and its
`Auto` mode restarts pods to apply a change. On a cluster of three small
workers, a few manual resizes a month are cheaper than running it. It stays an
option once client workloads change faster than the snapshots can follow.

Sizing by an average or a 95th percentile instead of the peak packs tighter,
but a container that reaches its peak above its request is the first to be
evicted when a node runs short. The peak plus 30 % keeps every container
inside its request in normal use, and the 2× limit absorbs what the snapshots
missed.

A CPU limit throttles a container in every scheduling period it overruns, even
on an idle node, so it costs latency and saves nothing. The cluster's own
services are trusted and measured; their CPU requests decide placement, and the
CFS share decides who wins when a node is busy. Shrinking the Redis caps to fit
the rule was weighed as well: it is more consistent on paper, but a session
store that hits a small cap fails writes for keys without a TTL, which users
would see as a logged-out session.

## What it costs

The rule needs data before it can set a number. A new workload starts from the
LimitRange default (`10m` / `64Mi`, limit `128Mi`) or from a guess, and is
resized once a few days of Prometheus data exist. Prometheus keeps only three
days on an `emptyDir`, so a longer view is a series of snapshots, and a restart
of the Prometheus pod resets the window.

Tighter requests leave less slack. A workload that grows without anyone
resizing it rises above its request and becomes an eviction candidate before it
reaches its limit. The rule holds only while someone reads the snapshots; an
alert on containers above their request, or the Vertical Pod Autoscaler's
recommender in read-only mode, would make that automatic and is the point to
revisit this decision.

## Amendments

**2026-11-07.** [ADR 0014](./0014-the-vertical-pod-autoscaler-sizes-the-clusters-own-workloads-and-node-memory-is-their-ceiling.md)
makes this rule the start value instead of the running value: the Vertical Pod
Autoscaler raises and lowers the requests of the cluster's own workloads from
there, with the floors above as its `minAllowed`. When those workloads move to
`InPlaceOrRecreate`, they also lose the memory limit of twice the request; the
node's memory, with a kubelet reserve, becomes their ceiling. PostgreSQL,
Redis, the JVMs and client namespaces keep this rule as written.
