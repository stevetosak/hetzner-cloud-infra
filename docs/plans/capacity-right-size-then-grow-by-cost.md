---
title: "Capacity: right-size, then grow by cost"
description: "How the cluster gets enough compute for paying workloads at the lowest cost: first reclaim the memory that requests reserve and nothing uses, then buy nodes by written triggers."
type: plan
status: active
topics: [kubernetes, provisioning, monitoring]
---

The workers are full on paper and mostly idle in fact. On 2026-11-01 the pods
on the three `cx23` workers requested about 9.1 GiB of memory, which put the
workers at 76 %, 77 % and 91 % of their allocatable memory. The same pods used
about 3.8 GiB at their peak, and about 0.6 of the 4.9 requested CPU cores.
Proven by Prometheus over about 2 hours (`kube_pod_container_resource_requests`
against `max_over_time(container_memory_working_set_bytes[…])`). Two hours of
a quiet cluster is not a peak, so the numbers below are a direction, not the
sizes.

The scheduler places pods by requests, not by use. A new workload is refused a
place long before the memory runs out, and the cluster looks like it needs a
fourth node it does not need. The largest reservations against their measured
peaks were PostgreSQL (3072 MiB requested, 525 MiB used), Envoy (1344 / 181),
the applications in `authos` (1312 / 435) and Argo CD (1216 / 675). The cause
is the house default of `200m` / `512Mi` per container, applied to small
services by habit. Grafana goes the other way: 256 MiB requested, 615 MiB used.

**The cheapest compute is the compute already paid for: right-size before any
node is bought.** Requests at the measured peak plus 30 % come to about
5.1 GiB, which fits on two workers (about 7.4 GiB allocatable) with room to
spare. The cluster then survives the loss of one worker (N+1) on the three
it has, and a first client fits without a new node.

Growth after that is a price question. Hetzner prices, excluding VAT, checked
over the API and the price pages on 2026-11-01:

| Server | Memory | €/month | €/GB | In stock |
|---|---|---|---|---|
| `cx23` (Cloud, shared) | 4 GB | 5.99 | 1.50 | `hel1` only |
| `cx33` (Cloud, shared) | 8 GB | 8.99 | 1.12 | nowhere |
| `cpx32` (Cloud, shared AMD) | 8 GB | 35.99 | 4.50 | `fsn1`, `nbg1`, `hel1` |
| `ccx13` (Cloud, dedicated vCPU) | 8 GB | 43.49 | 5.44 | `fsn1`, `nbg1`, `hel1` |
| AX41 (dedicated server) | 64 GB | 59.00 | 0.92 | yes, no setup fee |

A `cx23` is the cheapest Cloud memory that can be bought today, and it is
billed by the hour. A dedicated server is cheaper per GB above about 25 GB of
need, but it brings work: a vSwitch to the Cloud network, a node the Hetzner
CCM does not own, and no Cloud Volumes. **If `cx23` stock runs out in `hel1`,
the next Cloud option costs three times as much per GB, and the dedicated
server is cheaper from the first purchase.**

## Phase 1 — Right-size

**Done when:** every running container in `core/` and `projects/` carries a
memory request set by the sizing rule (no container is left without one), the
memory the workers' pods request fits on two workers (proven by a Prometheus
query), one worker has been drained with every pod Ready again, every
application namespace has a LimitRange, and `kubectl top nodes` answers
from metrics-server.

The sizing rule, to confirm in the first step: the memory request is the
measured peak working set times 1.3, rounded up to 16 Mi, and never under
32 Mi; the memory limit is twice the request. The CPU request is the measured
peak 5-minute rate times 1.3, never under `10m`, and the cluster's own
workloads carry no CPU limit (decided 2026-11-01, below). PostgreSQL keeps at least
384 Mi (128 MB `shared_buffers`, the CloudNativePG default, plus its working
memory), and Envoy keeps at least 128 Mi for traffic bursts.

- [ ] Measure the peaks over at least 7 days. Prometheus keeps only 3 days
      (`retention: 3d`, `retentionSize: 3GB`, on an `emptyDir` that a
      rescheduled pod loses), so a single 7-day query is not possible. Take
      three snapshots of the 3-day maximum per container (memory working set,
      CPU 5-minute rate, restarts and `OOMKilled`), on 2026-11-04, 2026-11-07
      and 2026-11-10. Check `prometheus_tsdb_lowest_timestamp` at each one to
      prove the window really covers 3 days. Record them in the Task Record.
- [ ] Settle the sizing rule and write it into the `Resource defaults` of
      `CLAUDE.md` and the kalfa `manifests` rule (its source module, not the
      built file). The `200m` / `512Mi` default goes, and so does the CPU
      limit. Record the rule and the CPU-limit choice as a Decision page.
- [ ] Right-size the applications: the `authos` and `doma` deployments under
      `projects/`. The `imaps` and `wasteio` manifests are not running and keep
      their values until they are deployed.
- [ ] Right-size the cluster services: Argo CD, Envoy Gateway and its
      `EnvoyProxy`, the CloudNativePG `Cluster`, Redis and reloader. Grafana's
      request is set by the monitoring Task on 2026-11-02.
- [ ] Give a request to every container that has none: cert-manager, the
      barman-cloud plugin, the hcloud CSI driver. A pod without requests is
      BestEffort, and the kubelet evicts it first under memory pressure.
- [ ] Add a LimitRange to every application namespace, so a manifest without
      resources gets a small default instead of none. The same object is the
      default for every future client namespace.
- [ ] Install metrics-server under `core/`, synced by Argo CD, with a pinned
      chart and image. It serves `kubectl top` and is the base for a
      HorizontalPodAutoscaler later.
- [ ] Prove N+1: confirm the requested memory fits on two workers, then drain
      one worker, wait for every pod to be Ready, and uncordon it. The drain is
      a write to the live cluster and runs only with the operator's yes.

## Phase 2 — Growth rules

**Done when:** a Reference page holds the triggers that say when to buy which
server, with the price table above, an alert fires when the workers lose N+1,
and `kluster node add` can add a worker of a server type other than the
configured default.

- [ ] Add a PrometheusRule that fires when the memory the workers' pods request
      no longer fits on all workers but one. That alert is the trigger to buy a
      worker, so a purchase follows a measurement and not a feeling.
- [ ] Add a `--server-type` flag to `kluster node add`. The worker set already
      stores a server type per worker (`internal/workerset`), but `node add`
      always takes `workers.serverType` from `kluster.yaml`, so a `cpx32` or a
      `cx33` (when back in stock) cannot be added beside the `cx23` workers.
- [ ] Measure the control plane's peak memory and record a trigger for a larger
      server. The `k8s-cp` node used about 60 % of its 4 GB on 2026-11-01, and
      `kube-apiserver` alone peaked at about 1.6 GiB without a memory request.
      A highly available control plane is a separate Plan.
- [ ] Write the Reference page `Capacity and growth`: the sizing rule, the N+1
      trigger, the order of purchase (`cx23` by the hour while in stock, then
      the dedicated path), the point where a dedicated server is cheaper, and
      the control-plane trigger. Prices on it carry the date they were checked.

## Decisions taken on 2026-11-01

**No CPU limits on the cluster's own workloads.** A CPU limit throttles a
container even when the node is idle, which costs latency and saves nothing.
Memory limits stay. A client namespace gets a CPU limit through its
LimitRange, because a client workload is not trusted the same way.

**metrics-server now, not later.** Prometheus already holds the data this Plan
needs, but autoscaling of the cluster's own workloads is planned, and a
HorizontalPodAutoscaler needs the metrics API. Installing it during
right-sizing adds one small service while the requests are being set anyway.

**The dedicated path is proven at the trigger, not before.** A trial month of
an AX41 (about €59) is out of budget while the cluster is paid out of pocket.
The unproven parts stay unproven until the buy trigger fires: the vSwitch, a
kubeadm join over it, the CCM's view of a node it does not own, and a taint
that keeps volume-bound pods (PostgreSQL) on Cloud nodes. The Reference page
lists them, so the work is known before the money is spent.
