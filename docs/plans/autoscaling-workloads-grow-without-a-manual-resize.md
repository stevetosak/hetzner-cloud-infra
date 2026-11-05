---
title: "Autoscaling: workloads grow without a manual resize"
description: "How a container's memory is raised before it runs out, once the right-sized requests leave little slack: the options weighed so far and the questions a design session has to settle."
type: plan
status: proposed
topics: [kubernetes, monitoring, gitops]
---

**Deferred on 2026-11-05: this work gets its own design session.** The
[Capacity Plan](./capacity-right-size-then-grow-by-cost.md) sets every
request from a measured peak
([ADR 0013](../adr/0013-requests-follow-measured-peaks-and-the-clusters-own-workloads-carry-no-cpu-limit.md)),
which removes the slack the old `512Mi` default gave by accident. The
applications see little traffic today, so their memory use will only go up,
and every rise past the request makes a container an eviction candidate; a
rise past the limit is an OOM kill.

Phase 1 of the Capacity Plan ships the notification: alerts in the Alert Chat
when a container's working set nears its request or its limit, and on any OOM
kill. What it does not ship is the raise. Today a person reads the alert and
changes the value in Git. This Plan makes the raise automatic, with the
notification first and the new value after it.

The difficulty is where the values live. A container's resources are written
in three kinds of source: Helm values files rendered by `render.sh` (Argo CD,
monitoring, cert-manager, metrics-server), Kustomize deployments (the
applications under `projects/`), and the CloudNativePG `Cluster`, whose pods
the operator owns. An automatic raise either edits all three in Git, or
changes the running pods and lets Git fall behind.

## Memory raise after an alert

**Done when:** a container whose working set nears its limit gets a higher
request and limit without a person editing a file, the Alert Chat says so
before or as it happens, Git and the live values agree again within a known
time, and the raise has been proven on live by driving one container over
its threshold.

- [ ] Decide the mechanism in a design session and record it as a Decision
      page. Options weighed on 2026-11-05:
      the Vertical Pod Autoscaler (kubernetes/autoscaler) in
      `InPlaceOrRecreate` mode raises request and limit on the running pod
      with no restart (in-place resize is GA on the cluster's v1.37), with a
      `minAllowed` per container at the ADR 0013 floors, PostgreSQL excluded,
      three more pods (about 200 MiB) and an admission webhook, and Git behind
      the live pods until a fold-back PR; an in-cluster job that opens a PR
      with the new value for the operator to merge, which keeps Git the truth
      but needs a writer for each of the three sources and raises nothing at
      night; or the alert alone with the suggested value.
- [ ] Build the chosen mechanism under `core/`, synced by Argo CD, with a
      pinned chart and image.
- [ ] Handle the capped processes: Redis keeps `maxmemory` below its limit
      and a JVM takes its heap from the limit (`duster` runs
      `-XX:MaxRAMPercentage=75`), so a raise there changes the process's own
      cap or is excluded.
- [ ] Prove it on live: one container driven past its threshold, the alert
      seen, the raise applied, Git brought back in line.

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

- Does an automatic raise need the operator's approval, or is the
  notification enough? A raise at night that waits for approval does not
  prevent the OOM kill.
- Does VPA also lower requests, which would make right-sizing continuous, or
  only raise them?
- How does a raise interact with the N+1 alert of the Capacity Plan's Phase 2:
  should a raise that breaks N+1 still happen?
