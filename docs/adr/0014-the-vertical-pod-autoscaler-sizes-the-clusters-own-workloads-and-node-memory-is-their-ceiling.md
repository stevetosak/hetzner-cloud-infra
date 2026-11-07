---
title: "The Vertical Pod Autoscaler sizes the cluster's own workloads, and node memory is their ceiling"
description: "Why the cluster's own containers are resized by the Vertical Pod Autoscaler instead of by hand, why they lose their memory limits, and what stays manual: PostgreSQL, Redis, the JVMs and every purchase."
type: decision
status: accepted
date: 2026-11-07
topics: [kubernetes, monitoring, gitops]
---

[ADR 0013](./0013-requests-follow-measured-peaks-and-the-clusters-own-workloads-carry-no-cpu-limit.md)
set every request from a measured peak, which removed the slack the old
`512Mi` default gave by accident. The applications see little traffic today,
so their memory use only goes up, and every rise past the request makes a
container the first candidate for eviction. A rise past the limit is an OOM
kill. The [Capacity Plan](../plans/capacity-right-size-then-grow-by-cost.md)
added alerts for both (`core/monitoring/rules/capacity.yaml`), but the raise
after the alert was a person editing Git.

The first days with the new values showed how often that person would be
needed. Right after the resize on 2026-11-05, four containers held above their
new request for their whole first hour and needed a second change (PR #88).
After a drain of `k8swk2` on 2026-11-07, freshly started pods used two to four
times the steady peak of the pods they replaced: the cloud controller manager
sat at 60 MiB against an 18 MiB peak, against a 64 Mi limit, and Grafana and
the ApplicationSet controller passed their requests. Each was a manual PR
(#97, #99). On 2026-11-05 the operator put the problem plainly: these hard
limits are not a way; node memory is the real ceiling, and a person should be
told when a pod is resized and when a node runs short, not asked to do the
resize.

What makes the choice hard to reverse is where the values live. A container's
resources are written in three kinds of source: Helm values files rendered by a
`render.sh` (Argo CD, monitoring, cert-manager, metrics-server), Kustomize
deployments (the applications under `projects/`), and the CloudNativePG
`Cluster`, whose pods the operator owns. Whatever does the raise either edits
all three in Git or changes the running pods and lets Git fall behind.

## The decision

**The Vertical Pod Autoscaler (kubernetes/autoscaler, chart
`vertical-pod-autoscaler` 0.15.0, VPA 1.8.1) sets the memory and CPU requests
of every own workload, raises and lowers them, and the own workloads carry no
memory limit: the node's memory, guarded by a kubelet reserve and soft
eviction, is their ceiling.** It runs under `core/vpa`, synced by Argo CD like
every other core directory.

Each workload gets one `VerticalPodAutoscaler` object, kept next to the
workload it sizes (`core/<dir>/vpa.yaml`, or beside the overlay under
`projects/`), so a workload that is removed takes its object with it. The
kube-system and kube-flannel workloads, which Argo CD does not manage, have
theirs in `core/vpa`. The kubeadm static pods (etcd, kube-apiserver,
kube-controller-manager, kube-scheduler) are left out: their requests live in
a manifest on the control plane. Client namespaces are left for later; they
keep the limits of their LimitRange.

The cluster services and the applications run in `InPlaceOrRecreate` mode:
the updater resizes a running pod in place (in-place resize is GA on the
cluster's Kubernetes v1.37) and recreates it only when the node cannot take
the new value. PostgreSQL, Redis and the two JVMs (`authos-api` with
`-Xmx256m`, `duster` with `-XX:MaxRAMPercentage=75`) run in `Off` mode: the
recommender writes its recommendation and nothing changes. Their memory is set
by their own cap (`shared_buffers`, `maxmemory`, the heap), so a larger
request does not give the process more room, and a CloudNativePG restart takes
the primary off writes for about 3.5 minutes.

Each object sets `minAllowed` at the ADR 0013 floors: 32 Mi and `10m` for any
container, 384 Mi for PostgreSQL and 128 Mi for Envoy. The recommender's own
floors are lowered to match (`--pod-recommendation-min-memory-mb` defaults to
250, which would grow every small pod). No CPU limit is added; ADR 0013 already
removed them.

ADR 0013 still sets the start value: the value in Git is what a new workload
starts from and what the cluster is rebuilt with. From there the live pods
lead and Git follows. A low-priority alert fires when a recommendation has been
more than twice or less than half the Git value for 7 days, and a person brings
Git up to date. Argo CD does not undo a resize: the updater changes the pod,
not the Deployment, and the admission webhook gives a recreated pod the
recommended value as it starts.

People are told rather than asked. Each resize is a notification, grouped by
namespace over 5 minutes (pod, container, old and new value). An Infeasible
resize, a pod left Pending and an OOM kill each alert at once. The node gets
its own alerts: available memory under 15 % for 5 minutes (warning) and under
8 % or MemoryPressure (critical), requests above 90 % of a node's allocatable
for 30 minutes (warning), and any eviction for memory pressure (critical).
The workers' cushion alert of the Capacity Plan (requests above 80 % of the
workers' memory) stays the signal to buy a worker. **Nothing is bought
automatically: every purchase waits for the operator's yes.**

The change lands in three steps. First VPA and every object run in `Off` for
3 days, and the recommendations are compared with the capacity snapshots that
set the ADR 0013 values. Then each worker gets its measured
`kubeReserved`/`systemReserved` and a soft eviction threshold through kluster,
one node at a time. Only then do the cluster services and applications move to
`InPlaceOrRecreate` and lose their memory limits: without the reserve, one
leaking container could fill its node before the kubelet acts (the operator
set this order on 2026-11-07). On 2026-11-06 a worker ran 64–78 MiB of
kubelet and 650–800 MiB of node working set outside the pods (mostly kernel
and page cache), with the kubeadm defaults: no reserve, hard eviction at
100 MiB available, no soft eviction (kubelet summary API).

## Why this and not the alternatives

Three ways to make the raise were weighed on 2026-11-05. An in-cluster job
that opens a PR with the new value keeps Git the only truth, which is the
strongest argument for it. It needs a writer for each of the three sources (a
values file and a re-render, a Kustomize patch, the CloudNativePG `Cluster`),
and it raises nothing until someone merges, so a rise at night still ends in
an eviction or an OOM kill by morning. The alerts alone, which already exist,
are the same thing with the writer replaced by a person.

VPA with the memory limits kept was the smaller step. It scales a limit with
its request, but a limit that follows a steady peak is too low for a fresh
start: the cloud controller manager's start reached 92 % of its limit before
PR #97 raised it, and a recommender cannot have seen a start that has not
happened yet. The node, with
a reserve the kubelet defends, is a ceiling that holds for every pod at once
instead of one guess per container.

VPA 1.8.1 also offers an `InPlace` mode that never recreates a pod. It is
alpha; `InPlaceOrRecreate` is GA since VPA 1.6 and falls back to a recreate,
which the PodDisruptionBudgets still govern. Moving to `InPlace` is one field
per object once it is stable.

## What it costs

The cluster runs three more pods, one each of recommender, updater and
admission controller (the chart's default of two each is cut to one), and an
admission webhook sits in the path of every pod creation. Its
`failurePolicy` is `Ignore`, so while it is down a pod starts with its Git
value and the updater corrects it later. Its certificate comes from
cert-manager, which the cluster already runs, so the render holds no
generated key.

Git is behind the live pods by design, by up to the 7 days of the drift
alert, and a rebuild starts every pod at its Git value again. A pod that
cannot be resized in place is recreated, which for a single-replica workload
is a short outage. Without a memory limit, one container that leaks takes
memory from every pod on its node until the kubelet evicts by priority. That
is the trade-off accepted, and it is why the kubelet reserve and the node
alerts are part of this decision, not an afterthought.

Raises also spend the workers' cushion. A raise that pushes the workers'
requests past 80 % fires the cushion alert, and the answer is a request
lowered or a worker bought by the operator, never a raise held back. The
choice is worth revisiting if the updater recreates pods often (in-place
resizes failing), if the drift alert stays open for weeks, or once client
namespaces are to be resized too.
