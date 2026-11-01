---
title: "Monitoring runs in the cluster, and a heartbeat outside watches it"
description: "Why the cluster gets kube-prometheus-stack with Grafana behind Cloudflare Access, why a dead-man's switch at healthchecks.io watches both it and the outside watcher, and what was left out."
type: decision
status: accepted
date: 2026-10-25
topics: [monitoring, kubernetes, security]
---

Nothing watches the inside of the cluster. `core/monitoring/lean-monitoring.yaml`
is a kube-prometheus-stack values file from before the loss of every server; it
was never installed in the rebuilt cluster, it pins its pods to a Worker name
that no longer exists, keeps Prometheus on a volume that
[ADR 0005](./0005-storage-and-worker-identity.md) took away, and commits the
Grafana password in plain text. There is no metrics-server either, so not even
`kubectl top` works. The only watcher is the outside one,
`recoverability-watch.yml`: a daily GitHub workflow that reads Hetzner and R2,
never the kube API, and reports to Telegram when the backups, the WAL, the drill
or the rehearsal age look wrong.

[ADR 0003](./0003-recoverability.md) handed three jobs to monitoring and left
them open. GitHub disables a scheduled workflow in a public repository after 60
days without activity, and says nothing, so the outside watcher can stop
without anyone noticing. A base backup taken from a standby does not wait for
WAL archiving, so a broken archiver passes every backup check and has to be
watched on its own. And the drill, the backups and the first certificate
renewal (due 2026-11-19, untested) need something that notices when they go
quiet. All three share one shape of fault: **a sender that is silent when all is
well is also silent when it is dead.** No amount of in-cluster alerting fixes
that for the cluster itself.

The room is small. Each of the three Workers has 2 vCPU and about 3.7 GiB;
`kubectl describe nodes` on 2026-10-25 showed memory requests at 59–79 % per
Worker, about 3 GiB free in total, and limits over-committed to about 150 %.
The choice was made in a design review on 2026-10-25, one question at a time,
each answer the operator's.

## The decision

**Monitoring has two layers: a lean kube-prometheus-stack inside the cluster,
and a dead-man's switch outside it that expects pings from both the cluster
and the outside watcher.** Each layer covers what the other cannot see. The
cluster sees the WAL archiver, pods, volumes and certificates, but cannot report
its own death; the outside switch sees nothing but missing pings, and that is
exactly what a dead cluster or a disabled workflow produces.

Inside, kube-prometheus-stack is installed in the shape of every other `core/`
directory: a pinned helm render with its `render.sh` and values file, its CRDs
split into a `crds.yaml` for the seed, and an Application `core-monitoring` in
the `core` ApplicationSet ([ADR 0010](./0010-argocd-syncs-core-and-kluster-only-seeds-it.md)).
Prometheus keeps three days of history on an `emptyDir`, as ADR 0005 already
said. No pod is pinned to a node; Prometheus and Alertmanager prefer different
nodes, so that losing one node does not take the rule engine and the sender at
once. The stale values file is replaced, not repaired.

The chart's own rules stay, minus a deny list of the ones that cannot work or
are known noise here. The cluster is built by kubeadm, which binds the metrics of
etcd, the scheduler and the controller manager to `127.0.0.1` on the Control
Plane (assumed from kubeadm's defaults; the build proves it), so their scrapes
and rules are turned off rather than rebinding them now; a dead Control Plane
still shows as the API server and a node going down. The over-commit rules go
too, because the cluster is over-committed by design. Custom rules add what the
chart does not know: WAL archiving failing, a certificate near expiry or not
ready, an unhealthy CNPG cluster, a CNPG volume filling up, and a public host
that does not answer. `critical` and `warning` alerts go to Telegram; `info`
stays in Grafana.

The public hosts are probed by blackbox-exporter from inside the cluster, out
through Cloudflare and back, so one probe tests DNS, Cloudflare, the origin
lock, the load balancer, the Gateway, the route and the pod. Every public host
gets a probe with its expected status, and `kluster services check` fails when
an applied route has no probe or a probe has no route — the same tool that
already stops drift in the list of things a new project must add.

Grafana is stateless. Its dashboards come only from git, as ConfigMaps the
chart's sidecar loads: the chart's Kubernetes dashboards plus pinned upstream
dashboards for CNPG, cert-manager, Envoy Gateway and ArgoCD. A dashboard made in
the UI survives only if its JSON is exported into the repository. The admin
password is a Generated Secret in the Secret Catalog, read through
`existingSecret`, because the chart otherwise renders a random one into the
committed render. Grafana is public at `grafana.tosak.net`, behind **Cloudflare
Access** and then its own login. The origin lock of
[ADR 0008](./0008-gateway-api-and-the-public-entry-point.md) is what makes
Access a real gate: no request reaches the Gateway without passing Cloudflare
first.

Outside, the dead-man's switch is **healthchecks.io** on its free plan, with two
checks made by hand and recorded in the runbook. The Alertmanager Watchdog, an
alert that always fires, pings the first every few minutes; it falls silent when
Prometheus, Alertmanager or the whole cluster is gone. The outside watcher pings
the second at the end of each daily run — a plain ping when every check was
clean, the `/fail` ping when any check found a fault. The second form makes
healthchecks.io an independent path for the watcher's findings, not only a sign
that it ran, so a failed Telegram send from the watcher no longer loses the
fault. The ping URLs are Issued Secrets held in Bitwarden.

Every alert goes to an **Alert Chat**: a new Telegram chat, written to by the
existing infra bot, that carries only alerts. Deploy messages stay in the old
chat. healthchecks.io sends to the Alert Chat too, so every "something is wrong"
message lands in one place, and a notification from that chat always means act.

In a rehearsal, monitoring runs through a Rehearsal Cut that removes its live
links: no Telegram receiver, no Watchdog ping, no probes of the live hosts.
The rehearsal then checks that every scrape target is up and that no alert but
the Watchdog fires, which makes monitoring a health test of every rebuilt
cluster.

## Why this and not the alternatives

Alerts alone, with no dashboards, would have cost less memory and no public
login; an outside-only design (heartbeat and uptime probes, nothing in the
cluster) would have cost almost nothing but could never see the WAL archiver,
which is the one gap ADR 0003 named as monitoring's own. The operator chose
dashboards as well, for trends and for debugging.

For the stack, victoria-metrics-k8s-stack reads the same monitors and rules with
two to four times less memory, but adds a second operator and a less common path
to debug; it stays the fallback if kube-prometheus-stack does not fit once
measured. Grafana Cloud's free tier would have taken the store, the alerts and
the dashboards out of the cluster, so they would outlive it, but it moves the
design out of the repository's own IaC, needs another account and key, and its
series limit is below what a default Kubernetes scrape produces.

For access, a public route with only Grafana's login is how ArgoCD is exposed
today, and it leaves one password between the internet and a page that maps the
whole cluster. A port-forward over the VPN exposes nothing but works only from
the laptop. Access costs nothing on the free plan and keeps the phone.

For the switch, a Cloudflare Worker with a cron trigger would have avoided a new
account, but the last line of watching would then be code written and kept here,
when a service built for exactly this job exists, is open source, and sends to
Telegram natively. Cronitor, Better Stack and Dead Man's Snitch have smaller free
tiers and more product than the job needs. Defining the two checks as code (a
community Terraform provider, or kluster calling the API) buys reproducibility
for two objects that a rebuild never destroys.

Keeping every chart rule would page from the first day — the Control Plane
scrapes and the over-commit rules fire at once — and teach the reader to ignore
the chat. Writing every rule by hand would throw away good defaults (crash
loops, a node not ready, a failed Job) only to rewrite them. Pinning the pods to
one Worker made sense while Prometheus kept a volume; on an `emptyDir` it only
makes monitoring go down with the node it should be reporting. A volume for
Grafana, or its database in the shared PostgreSQL cluster, would keep
dashboards made in the UI, but the first is lost with the cluster anyway and the
second makes Grafana depend on the database it is often used to debug.

Logs were left out. Loki and an agent on every node would add roughly 0.5 to
0.9 GiB to a cluster with about 3 GiB free and no way yet to measure real use,
and none of the three jobs ADR 0003 handed over needs logs.

## What it costs

About 0.8 to 1 GiB of memory requests in a cluster with about 3 GiB free (an
estimate from the chart's usual footprint; the build measures it). Prometheus
history is lost on every restart and every move. Two outside services join the
design — healthchecks.io and Cloudflare Access — each with a free plan that is
assumed, not proven, to be enough; the build checks both before relying on
them, and also checks that Cloudflare's bot protection does not answer the
probes in place of the origin, and that the WAL rule's metric is fed under
plugin archiving. The deny list has to be read again on each chart upgrade,
because a renamed rule comes back on. A dashboard worth keeping has to be
exported by hand.

Three things were deferred, not rejected: logs, decided with real memory numbers
from Grafana and worth taking up once a fault cannot be diagnosed because a
pod's logs were gone; Cloudflare Access in front of `argocd.tosak.net`; and
rebinding the Control Plane metrics so their rules can come back. The choice is
worth revisiting if the stack does not fit in memory (victoria-metrics), if the
free plans change, or if the cluster grows enough that a hosted store becomes
cheaper than the memory it takes.


## Amendments — 2026-11-01, found in the branch review

**The decision does not change.** This is a correction of one phrase.

**1. "No alert but the Watchdog" means no alert that would page on live.** The
rehearsal check above says that no alert but the Watchdog fires. Read
literally, that fails every rehearsal: the `InfoInhibitor` always fires, and
info alerts such as `CPUThrottlingHigh` fire on a small cluster without a fault.
Neither pages anybody, because the live routing sends them to `null`. The check
therefore counts exactly the alerts that the live routing sends to Telegram:
every alert except the Watchdog, the `InfoInhibitor` and those of severity
`info`. An alert with no severity counts, because the default route sends it to
Telegram as a rule fault.

The first version of the check counted only `warning` and `critical`, so a
rehearsal could pass with an unlabelled alert that would page on live. The
branch review found the gap; the check now follows the routing, and a unit test
holds an alert with no severity. The [services-seed
runbook](../runbook/services-seed.md#monitoring-clean) describes the stage.
