---
title: "Capacity and growth"
description: "How the cluster's memory is sized, which alerts say it is time to buy a server, what each server costs, and the order in which servers are bought."
type: reference
topics: [kubernetes, provisioning, monitoring]
---

The rules come from the [Capacity Plan](../plans/capacity-right-size-then-grow-by-cost.md)
and [ADR 0013](../adr/0013-requests-follow-measured-peaks-and-the-clusters-own-workloads-carry-no-cpu-limit.md).
The alerts are in [`rules/capacity.yaml`](../../core/monitoring/rules/capacity.yaml).
**No alert buys anything: every purchase is the operator's decision.**

## Sizing rule

| Value | Rule |
|---|---|
| Memory request | Peak working set × 1.3, rounded up to 16 Mi, at least 32 Mi |
| Memory limit | 2 × the request |
| CPU request | Peak 5-minute rate × 1.3, at least `10m` |
| CPU limit | None on the cluster's own workloads; a client namespace gets one from its LimitRange |
| Peak window | At least 3 days of Prometheus data (`python3 -I core/monitoring/capacity-snapshot.py 3d`) |
| New component | `50m` / `256Mi`, limit `512Mi`, resized once 3 days of data exist |
| Own memory cap (Redis `maxmemory`, a JVM heap) | The limit follows the cap; the request follows the rule |
| Floors | PostgreSQL 384 Mi, Envoy 128 Mi |

A fresh start can use much more memory than the steady peak: after the drain
on 2026-11-07 Grafana started at 627 MiB against a 318 MiB steady peak, and
the hcloud CCM at 60 MiB against 18. The next start of the same pods stayed
at 421 and 17 MiB. A value sized from one start is a ceiling guess.

## Triggers

| Alert | Fires when | For | Action |
|---|---|---|---|
| `WorkersMemoryCushionLow` | The workers' pods request more than 80 % of the workers' allocatable memory | 1 h | Lower a request above its measured peak, or buy a worker by the order of purchase |
| `ControlPlaneMemoryHigh` | The control plane uses more than 90 % of its memory (MemTotal − MemAvailable) | 1.5 h | Find what grew; if it is growth and not a leak, consider a larger control-plane server |
| `ContainerMemoryNearRequest` | A container uses more than 90 % of its memory request | 30 min | Resize the container by the sizing rule |
| `ContainerMemoryNearLimit` | A container uses more than 85 % of its memory limit | 10 min | Raise its limit now |
| `ContainerOOMKilled` | A container restarted after an OOM kill in the last 15 min | — | Raise its limit; check a JVM's or Redis's own cap |

The cushion share is a sum over all workers, and a sum never proves that one
pod fits on one node. On 2026-11-07 a drain of `k8swk2` left 816 MiB free
across the two other workers, but the PostgreSQL replica (544 Mi) fitted on
neither. N+1 is not a requirement; the 80 % cushion replaced it that day.

`ControlPlaneMemoryHigh` watches use, not requests, because the kubeadm static
pods carry little or no request. Both trigger alerts were loaded with health
`ok` and inactive on 2026-11-07 (Prometheus rules API, 17:45 UTC).

## Current capacity

All four nodes are `cx23` in `hel1-dc2` (`kubectl get nodes`), each with
3720 MiB allocatable. On 2026-11-07, 17:40 UTC, the workers' running pods
requested 7648 MiB of 11160 MiB, 69 % (Prometheus,
`namespace_pod_container:memory_requests:bytes`):

| Node | Requested |
|---|---|
| `k8swk1` | 3328 MiB |
| `k8swk2` | 2032 MiB |
| `k8swk3` | 2288 MiB |
| `k8s-cp` | 400 MiB |

## Control plane

`k8s-cp` has 3820 MiB of memory. Measured by Prometheus from 2026-11-05
21:02 to 2026-11-07 17:45 UTC, the whole window it held (it keeps 3 days on
an emptyDir and restarts lose it):

| Measure | Value |
|---|---|
| Used memory, average | 62 % |
| Used memory, peak | 67 % (about 2570 MiB) |
| `kube-apiserver` peak working set | 1811 MiB, no request |
| `kube-controller-manager` peak | 107 MiB |
| `etcd` peak | 96 MiB of a 100Mi request |
| Requested on the node | 400 MiB |

The 90 % trigger sits at about 3440 MiB used, so about 870 MiB above the
measured peak. A larger control-plane server means an 8 GB type in `hel1`;
on 2026-11-07 only `cpx32` and `ccx13` were in stock there (`cx33` was in
stock nowhere). A server type change powers the server off, so the API
server is down while it runs; the workloads keep running (assumed, not
tested). A highly available control plane is a separate Plan.

## Prices

Hetzner prices excluding VAT, checked 2026-11-07: Cloud servers from the
Cloud API (`/v1/server_types`, `/v1/pricing`), the AX41 from its product
page. A Cloud server's price here includes its primary IPv4 (€0.50 a month),
which every node has. The prices equal those of 2026-11-01.

| Server | Memory | €/month | €/GB | In stock on 2026-11-07 |
|---|---|---|---|---|
| `cx23` (Cloud, shared) | 4 GB | 5.99 | 1.50 | `nbg1` only, not `hel1` |
| `cx33` (Cloud, shared) | 8 GB | 8.99 | 1.12 | nowhere |
| `cpx22` (Cloud, shared AMD) | 4 GB | 19.99 | 5.00 | `fsn1`, `nbg1`, `hel1` |
| `cpx32` (Cloud, shared AMD) | 8 GB | 35.99 | 4.50 | `fsn1`, `nbg1`, `hel1` |
| `ccx13` (Cloud, dedicated vCPU) | 8 GB | 43.49 | 5.44 | `fsn1`, `nbg1`, `hel1` |
| AX41 (dedicated server) | 64 GB | 59.00 | 0.92 | `FSN1`, `HEL1`, no setup fee |

One AX41 costs as much as about ten `cx23` (€59.90) or 1.6 `cpx32`.

## Order of purchase

1. A `cx23` worker in `hel1`, billed by the hour, while it is in stock:
   `kluster node add` takes `cx23` by default.
2. When `cx23` is out of stock in `hel1`, as on 2026-11-07, the next Cloud
   option (`cpx32`) costs three times as much per GB, and the AX41 is cheaper
   per GB from the first purchase. The operator decides with the stock of that
   day. `kluster node add --server-type <type>` accepts a type only when it is
   offered in the control plane's location.
3. A `cx23` in `nbg1` is not an option today. `nbg1` is in the same network
   zone, `eu-central`, so the private network could reach it (assumed, not
   tested), but a Cloud Volume attaches only to a server in its own location,
   so a pod with a `hel1` volume, such as PostgreSQL, cannot run there
   ([ADR 0005](../adr/0005-storage-and-worker-identity.md)). kluster adds
   workers only in the control plane's location.

The dedicated path is proven when the trigger fires, not before. Unproven
until then: the vSwitch to the Cloud network, a kubeadm join over it, the
hcloud CCM's view of a node it does not own, and a taint that keeps
volume-bound pods on Cloud nodes.
