---
title: "Autoscaling: workloads grow without a manual resize"
description: "The steps that let the Vertical Pod Autoscaler raise and lower the cluster's own containers before they run out, with node memory as their ceiling, and then let the first application scale its replicas on load."
type: plan
status: active
topics: [kubernetes, monitoring, gitops]
---

The [Capacity Plan](./capacity-right-size-then-grow-by-cost.md) set every
request from a measured peak
([ADR 0013](../adr/0013-requests-follow-measured-peaks-and-the-clusters-own-workloads-carry-no-cpu-limit.md)),
which removed the slack the old `512Mi` default gave by accident, and added
alerts when a container nears its request or its limit. The raise after the
alert was still a person editing Git, and in the first days it was needed
three times (PRs #88, #97, #99). The design session of 2026-11-05 chose the
Vertical Pod Autoscaler for the raise and the node's memory as the ceiling;
[ADR 0014](../adr/0014-the-vertical-pod-autoscaler-sizes-the-clusters-own-workloads-and-node-memory-is-their-ceiling.md)
records the choice and the options it beat. This Plan builds it, in the order
the ADR sets: everything in `Off` for three days first, then the kubelet
reserve, then resizes and no memory limits.

## The Vertical Pod Autoscaler sizes the own workloads

**Done when:** VPA runs under `core/vpa`, synced by Argo CD; every own workload
(static pods and client namespaces excepted) has a `VerticalPodAutoscaler`
object; the cluster services and applications run in `InPlaceOrRecreate`
without memory limits while PostgreSQL, Redis and the JVMs stay in `Off`; the
resize notifications and node alerts are live; each worker has a measured
kubelet reserve and soft eviction threshold; and a resize has been proven on
live by driving one container past its request.

- [x] Decide the mechanism in a design session and record it as a Decision
      page. Done 2026-11-07:
      [ADR 0014](../adr/0014-the-vertical-pod-autoscaler-sizes-the-clusters-own-workloads-and-node-memory-is-their-ceiling.md),
      from the operator's answers of 2026-11-05 (VPA in mixed modes, no memory
      limits on own workloads, raise and lower, Git as the start value with a
      drift alert) and of 2026-11-07 (objects next to their workload).
- [x] Install VPA 1.8.1 under `core/vpa` from the pinned chart
      `vertical-pod-autoscaler` 0.15.0 through a `render.sh`: one replica each
      of recommender, updater and admission controller, the webhook
      certificate from cert-manager, the recommender's floors lowered to
      32 MiB and `10m`, the CRDs in `crds.yaml` for `kluster services seed`,
      and `core-vpa` in the `core` ApplicationSet. Done 2026-11-08
      (PR #101, then `kluster services seed`): the three pods Ready and
      `core-vpa` Synced and Healthy. The Application had used up its sync
      retries while the namespace was missing and needed one manual sync
      after the seed created it.
- [x] Add one `VerticalPodAutoscaler` per own workload in `Off`, with
      `minAllowed` at the ADR 0013 floors: next to the workload in
      `core/<dir>/` and under `projects/`, and in `core/vpa` for the
      kube-system and kube-flannel workloads. Done 2026-11-08
      (PR #104): 38 objects, `RequestsOnly`; Prometheus, Alertmanager and
      the CloudNativePG `Cluster` are targeted through their custom
      resources, the top-level owners of their pods. Proven on live: every
      object reports `RecommendationProvided` and none `ConfigUnsupported`
      (`kubectl get vpa -A`). The three days in `Off` run from 2026-11-08.
- [x] After three days in `Off`, compare each recommendation with the capacity
      snapshots and the current requests, and record the result:
      [`vpa-compare.py`](../../core/monitoring/vpa-compare.py) prints both side
      by side (`python3 -I core/monitoring/vpa-compare.py 3d`). Done
      2026-11-14 over 2026-11-11 12:00 to 11-14 17:40 UTC: every memory target
      is at or above its container's peak in the window. No pod started in
      that window, so it holds no start peaks; the 2026-11-05..08 snapshot
      showed four containers whose start peak is above their target (Grafana
      650 MiB against 488, the Envoy shutdown-manager 108 against 35, the CSI
      sidecars 57-64 against 32-35, the Hetzner cloud controller 60 against
      32). The operator accepted those targets without a higher `minAllowed`:
      the kubelet reserve comes before the memory limits go, so a start peak
      uses node memory for seconds, about 400 MiB if all of them start
      together. The compare also showed `argocd-server` at 109 MiB on an
      80 Mi request; its request became 144 Mi in the same change.
- [x] Add the alerts: each resize as a notification grouped by namespace over
      5 minutes; an Infeasible resize, a Pending pod and an OOM kill at once;
      node memory available under 15 % (warning) and under 8 % or
      MemoryPressure (critical); a node's requests above 90 % of allocatable
      for 30 minutes; and any memory-pressure eviction. They are
      `rules/autoscaling.yaml` with the updater's PodMonitor
      `monitors/vpa.yaml`, and the OOM kill is the existing
      `ContainerOOMKilled`; the chart's `NodeMemoryHighUtilization` is in the
      deny list, replaced by `NodeMemoryAvailableLow`. A recommendation more
      than twice or less than half its Git value gets no alert: no metric
      holds a workload template's request, because kube-state-metrics' custom
      resource state reads only CRD kinds. `vpa-compare.py` shows the
      template's request in its `git` column and flags the drift, and the
      alert waits as a deferred item (operator, 2026-11-15).
      Done 2026-11-15 (PR #138): the target `monitoring/vpa-updater` is up,
      the groups `vpa` and `nodes` load with all 15 rules healthy, none of
      the new alerts fires, and Alertmanager's loaded config holds the
      `telegram-notice` route (Prometheus and Alertmanager APIs). The
      updater's counters appear only at the first resize, so the
      notification itself is proven by the last step.
- [x] Measure the kubelet and system memory on each worker and set
      `kubeReserved`, `systemReserved` and a soft eviction threshold through
      kluster, one node at a time.
      Done 2026-11-16 (PR #141, `kluster node kubelet-config`): k8swk2, k8swk1
      and k8swk3 run 160Mi / 480Mi, soft eviction at 300Mi after 1m; each
      Worker's allocatable memory is 3079 MiB, all Ready, no pod rejected
      (`configz`, `kubectl get node`). k8swk3 needed room first; the cordon
      for it switched the PostgreSQL primary over
      ([workers runbook](../runbook/workers.md#set-the-kubelet-reserve)).
- [x] Move the cluster services and applications to `InPlaceOrRecreate` and
      remove their memory limits, once every worker has its reserve.
      PostgreSQL, Redis (`maxmemory` below its limit) and the JVMs
      (`authos-api` at `-Xmx256m`, `duster` at `-XX:MaxRAMPercentage=75`)
      stay in `Off` and keep their limits.
      Done 2026-11-16 in three PRs: the applications and their LimitRanges
      (#143), the cluster services (#144), Argo CD and Envoy (#146). Live, no
      container carries a memory limit except PostgreSQL with its barman
      sidecar, Redis, argocd-redis, Stalwart, the JVMs and the `kube-system`
      and `kube-flannel` components (`Off`); every other VPA is
      `InPlaceOrRecreate` with `RecommendationProvided` and no
      `ConfigUnsupported` (`kubectl get pods,vpa -o json`). The updater has
      resized Grafana, the cert-manager webhook, metrics-server and the Argo CD
      repo-server among others in place, without a restart (events
      `InPlaceResizedByVPA`, `ResizeCompleted`). Envoy rolled with no failed
      probe of a public host. The chart of `envoy-gateway` merges its 1024Mi
      default limit back when the values leave `limits` out; `limits: null`
      removes it.
- [ ] Prove it on live: one container driven past its request, the resize
      seen in the Alert Chat, and Git brought back in line.

## Horizontal scaling

**Done when:** at least one application scales its replicas on load through a
HorizontalPodAutoscaler, proven by a load test that adds and then removes a
replica.

- [ ] Decide which workloads can run more than one replica (session state,
      `strategy: Recreate`, database migrations at start) and record it.
- [ ] Add a HorizontalPodAutoscaler to the first such workload. metrics-server
      already serves the metrics API it needs
      ([`core/metrics-server`](../../core/metrics-server/README.md)).

## Open questions

- When do client namespaces get VPA objects, and do they then keep the CPU
  and memory limits of their LimitRange?
