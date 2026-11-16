---
title: "Capacity: reclaim requests, then grow"
description: "How the workers get room for more applications: first the memory that requests reserve and nothing uses, then a purchase that is ready before its trigger fires."
type: plan
status: active
topics: [kubernetes, provisioning, monitoring]
---

The workers are full on paper again. On 2026-11-16 at 08:10 UTC the pods on
the three `cx23` workers requested 7772 MiB of 9237 MiB allocatable, 84 %,
and used about 3800 MiB (kubectl pod and VPA objects, Prometheus working
set). `WorkersMemoryCushionLow` fired at 07:45 UTC that day. Two changes
since the [first Capacity Plan](capacity-right-size-then-grow-by-cost.md)
closed brought the share back up: the kubelet reserve took about 640 MiB of
allocatable memory from each worker, and the Vertical Pod Autoscaler raised
Grafana from 528 to 933 MiB requested
([Autoscaling Plan](autoscaling-workloads-grow-without-a-manual-resize.md)).
A new application finds no place while the cushion is below a fifth.

The largest gaps between requested and used memory, in MiB on 2026-11-16:

| Workload | Requested | Used | Who sets the request |
|---|---|---|---|
| PostgreSQL, 3 instances | 1152 | 312 | Git (VPA `Off`), 384 Mi floor each |
| barman-cloud sidecars, 3 | 480 | 76 | Git, on the `ObjectStore` |
| Grafana | 933 | 419 | VPA (`InPlaceOrRecreate`) |
| Prometheus | 933 | 551 | VPA; its earlier 3-day peak was 802 |
| Stalwart | 256 | 48 | Git (VPA `Off`); VPA target 61 |
| hcloud CCM | 80 | 20 | Git, applied by hand |

Prometheus history starts at 2026-11-16 06:44 UTC (its restart that day lost
the `emptyDir`), so the "used" column is a few hours of a quiet cluster and
the sizes below are checked against 3 days before each change. Prometheus
keeps its request: the VPA target holds a measured peak.

**Buying a worker is the expensive path this week: `cx23` and `cx33` were out
of stock on 2026-11-16** (checked by the operator), and the next options cost
three times as much per GB (`cpx32`) or bring the unproven dedicated path
(AX41), as the [Capacity and growth](../reference/capacity-and-growth.md)
page lists. Reclaiming comes first because it is free. The purchase is
prepared in the second part so that, when the trigger fires, the decision
takes one look and not a week of work. No step here buys anything; every
purchase is the operator's decision.

## Reclaim requests

**Done when:** the workers' pods request at most 72 % of the workers'
allocatable memory, measured by Prometheus one day after the last change of
this part; `WorkersMemoryCushionLow` is inactive; every resized container
passed its first-hour check; and the
[Capacity and growth](../reference/capacity-and-growth.md) page shows the new
allocatable memory, the requests and the PostgreSQL instance count.

The estimate is about 6380 MiB requested, 69 %, which leaves about 1 GiB below
the 80 % cushion for the next application.

- [ ] PostgreSQL from 3 to 2 instances: one primary and one asynchronous
      replica (operator, 2026-11-16). Failover still works; a second fault
      while the replica rebuilds stops the database until it recovers, and
      the backups in R2 with the monthly restore drill stay the recovery
      path. About 544 MiB back (postgres and its sidecar) and one 10Gi
      volume. `instances` changes in every variant of the Cluster file
      (`fresh`, `recover`) and in the rehearsal cut; the Cluster is applied by
      hand, with the operator's yes. Proof: two instances streaming, the
      removed instance's PVC and Hetzner volume gone, and a WAL segment
      archived after the change.
- [ ] The barman-cloud sidecar sized from a window that holds a daily base
      backup. Its 160 Mi request was set from a 120 MiB peak on 2026-11-07;
      it uses about 25 MiB between backups. About 190 MiB back if the backup
      peak allows about 64 Mi. The value lives on the `ObjectStore`, and a
      change there rolls the Cluster, so it runs in the same slot as the
      instance change when the measurement is ready by then.
- [ ] Grafana capped by its VPA: a memory `maxAllowed` of about 512 Mi, so
      the recommendation stops following its start spike. Grafana has no
      memory limit since the move to `InPlaceOrRecreate`, so a start above
      the request is not killed while the node has room. About 420 MiB back.
- [ ] Stalwart sized by ADR 0013 once 3 days of data exist (from 2026-11-19
      06:44 UTC): from 256 Mi towards its VPA target of 61 Mi. About
      190 MiB back.
- [ ] The small items by the same rule: the hcloud CCM (80 Mi requested,
      20 MiB used) and any container whose request is more than twice its
      3-day peak, about 50 MiB in total. `core/hcloud` is applied by hand.
- [ ] Measure the share one day after the last change and update the
      Capacity and growth page: allocatable memory per worker after the
      kubelet reserve, the requests per node, the PostgreSQL instance count.

## Prepare the purchase

**Done when:** the Capacity and growth page compares `cpx32` and the AX41
with prices and stock checked on the day of this part, names the memory need
at which the AX41 becomes the cheaper choice, and states what the dedicated
path still needs before an AX41 can join; and a `kluster node add
--server-type cpx32` run in Plan Mode reads the type as available in `hel1`.

`WorkersMemoryCushionLow` stays the trigger. When it fires after the reclaim,
it is reported to the operator, who decides the purchase from this page.

- [ ] Check the prices and the stock of `cx23`, `cx33`, `cpx32` and the AX41
      over the Cloud API and the AX41 product page, and update the price
      table with the date.
- [ ] Write the comparison on the Capacity and growth page: the monthly
      cost per GB of usable worker memory after the kubelet reserve, the
      break-even point between more `cpx32` and one AX41, and the order of
      purchase for the case that `cx23` stays out of stock.
- [ ] Prove the `cpx32` path in Plan Mode (`kluster node add --server-type
      cpx32`), with no server created.

## Open questions

- An ARM worker (`cax`) and a `cx23` in another `eu-central` location are not
  compared now (operator, 2026-11-16). Every platform image has an `arm64`
  variant, but the application images (authos, doma, tosak-net-site) are
  `amd64` only (`docker manifest inspect` on the 49 running images,
  2026-11-16), so an ARM worker would need multi-arch builds in the app CI or
  an `amd64` node affinity. A worker outside `hel1` cannot attach the `hel1`
  volumes and could carry stateless pods only.
- A lower VPA recommender percentile or safety margin gives the most room
  without a purchase, at the risk of out-of-memory kills and evictions.
  Grafana and Prometheus, the two largest, start well above their steady use.
- A smaller monitoring stack (VictoriaMetrics in place of Prometheus, fewer
  series) would reclaim more of the 2427 MiB that `monitoring` requests, but
  it reopens [ADR 0011](../adr/0011-monitoring-runs-in-the-cluster-and-a-heartbeat-outside-watches-it.md).
