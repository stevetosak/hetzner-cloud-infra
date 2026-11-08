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
- [ ] After three days in `Off`, compare each recommendation with the capacity
      snapshots and the current requests, and record the result.
- [ ] Add the alerts: each resize as a notification grouped by namespace over
      5 minutes; an Infeasible resize, a Pending pod and an OOM kill at once;
      node memory available under 15 % (warning) and under 8 % or
      MemoryPressure (critical); a node's requests above 90 % of allocatable
      for 30 minutes; any memory-pressure eviction; and a recommendation more
      than twice or less than half its Git value for 7 days.
- [ ] Measure the kubelet and system memory on each worker and set
      `kubeReserved`, `systemReserved` and a soft eviction threshold through
      kluster, one node at a time.
- [ ] Move the cluster services and applications to `InPlaceOrRecreate` and
      remove their memory limits, once every worker has its reserve.
      PostgreSQL, Redis (`maxmemory` below its limit) and the JVMs
      (`authos-api` at `-Xmx256m`, `duster` at `-XX:MaxRAMPercentage=75`)
      stay in `Off` and keep their limits.
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
