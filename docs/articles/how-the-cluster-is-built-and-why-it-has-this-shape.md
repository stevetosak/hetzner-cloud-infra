---
title: "How the cluster is built, and why it has this shape"
description: "A tour of the rebuilt cluster layer by layer — servers, networks, the way in, the data and how it is proven, how applications arrive — and the 2026-09-13 loss that decided each layer's shape."
type: article
asOf: 2026-10-03
topics: [kubernetes, provisioning, networking, ingress, backups, gitops]
---

On 2026-09-13 a single command meant to add one Worker destroyed every server
in the cluster. The control plane went first, six seconds ahead of any Worker,
and with the servers went four PostgreSQL databases, every volume and every
object that had lived only in the cluster's own memory. There were no backups
of any kind. What exists now was rebuilt from zero, starting on 2026-09-20, and
almost every part of it is shaped by one question the loss made unavoidable:
what has to survive when everything else is gone?

This page follows that question through the cluster, one layer at a time. It
describes the shape and the reasons for it; the values, the commands and the
evidence live in the pages it links to, which own them. The vocabulary — Worker,
Control Plane, Public Entry Point, Route and the rest — is defined once in the
[glossary](../../CONTEXT.md), and each term there has exactly one meaning.

## The loss decided the shape

The damage did not come from one fault but from a chain, and every link in it
held weight. PostgreSQL stored its data through Longhorn, which keeps volumes on
the Workers' own disks. Longhorn ties a disk to the name of the server it sits
on, so a recreated Worker could not reuse its old name, and the automation gave
every Worker a suffix that changed with each creation. That suffix was shared by
all Workers, so adding one renamed them all — and at the provider, a renamed
server is a replaced one. Every replica of every database sat on the servers
being replaced. The chain is laid out in
[Decision 0005](../adr/0005-storage-and-worker-identity.md), and the parts of it
that sat in the provisioning tool in
[Decision 0004](../adr/0004-kluster-safety-model.md).

Behind the chain was a contradiction that had existed long before the incident.
Workers were meant to be disposable, and the data lived on the set of Workers.
Read literally, every full reset of the Workers ever run had destroyed every
database. The glossary now states the distinction in one line — a Worker is
ephemeral; the Worker set is not — because blurring the two is what made the
loss expensive.

Two choices followed before any server was created. The cluster was rebuilt
clean rather than restored from an old disk snapshot of the control plane, so
that everything it runs is written down rather than inherited. And the exact
trigger for the control plane's deletion, which was never proven, was left
unproven: the remedies described below hold whichever of the possible causes it
was ([Decision 0002](../adr/0002-control-plane-isolation.md)).

## Servers that can be thrown away, and one that cannot

The cluster has one Control Plane and three Workers on Hetzner Cloud. They are
described by three separate Terraform root modules, each with its own state:
one for what everything shares — the network, the firewalls, the key, the
control plane's public address — one for the Control Plane, and one for the
Workers. The split exists so that an operation on Workers cannot even express
the Control Plane. Before the loss, one state file held all of it, and the only
thing between a Worker change and the Control Plane was someone reading a plan
carefully.

Because data no longer lives on Worker disks, Worker names are stable again,
and the suffix machinery that caused the loss was deleted rather than fixed. A
Worker can be destroyed and recreated as a matter of routine; its identity is
its name, not the server currently answering to it. The `kluster` command-line
tool, which drove the fatal run, survives as an assistant only: it shows the
plan it intends, checks that plan against what the command claims to do, and
acts only when told to. The written runbooks for the
[Control Plane](../runbook/control-plane.md) and the
[Workers](../runbook/workers.md) are the authority on how a server is built.

The Control Plane is the one server that cannot be thrown away — nothing yet
rebuilds it automatically, and other parts of the system expect its addresses.
One preparation for changing that was made at the start. The API server answers
to a name of the cluster's own, resolved on each server and absent from public
DNS, rather than to an address. A second Control Plane later means pointing
that name at something that fronts both, without re-issuing certificates or
rebuilding the cluster ([Decision 0006](../adr/0006-network-paths-and-control-plane-endpoint.md)).

## Three networks, each with one job

Every server has three interfaces, and each carries one kind of traffic. The
Hetzner private network carries everything the cluster says to itself: kubelets
to the API server, pod to pod, and the load balancer to the entry point. A
WireGuard VPN, with the Control Plane as its hub, carries operator traffic
only, which is why no server needs SSH open to the world outside a bootstrap.
The public interface carries outbound traffic — packages, images — and the one
inbound port the VPN needs.

The rule is simple to state and was easy to lose. In the lost cluster it was
written down nowhere, and it survived only in a handful of flags. Auditing it
for the rebuild found six separate settings, spread over the API server, the
kubelet, the cloud controller, the pod network, the load balancer and each
server's hosts file, each of which points the wrong way if left at its default,
and none of which reports the mistake. The pod network, Flannel, would have
picked the public interface by itself. Decision 0006 names all six.

One finding changed what "private" means here. Hetzner's firewalls see only the
public interface; traffic inside the private network passes unfiltered. The
private network is therefore an addressing rule, not a security boundary, and
the glossary says so. Filtering inside it would need host firewalls or
Kubernetes NetworkPolicy, which in turn needs a different pod network. That is
why a Cilium migration is planned for and not done: an address range is kept
free for it, and the trigger for spending it is the first real need for
NetworkPolicy ([Decision 0001](../adr/0001-cluster-network-plan.md)).

## One way in

All traffic from outside arrives through a single Public Entry Point. Visitors
reach Cloudflare first, since every public host is proxied; Cloudflare connects
to one Hetzner load balancer; the load balancer hands connections, with PROXY
protocol, to Envoy Gateway inside the cluster. The previous controller,
ingress-nginx, was retired upstream in March 2026, and the rebuild was the one
moment its replacement cost nothing: no traffic was live and the load balancer
was due to be recreated anyway
([Decision 0008](../adr/0008-gateway-api-and-the-public-entry-point.md)).

The entry point belongs to the organisation, not to any application. It holds
the listeners, the load balancer and one wildcard certificate, issued through
DNS before the load balancer existed, so the first request it served already
had valid TLS. An application brings only a Route: its claim on a hostname and
a path, kept beside its workload. The Gateway names which namespaces may attach
a Route at all, which is a deliberate change from the old shape, where any
namespace could claim any host. The build is recorded step by step in the
[cluster services runbook](../runbook/cluster-services.md).

Building this layer corrected the decision behind it seven times — field names,
an ordering, a claim about which features are stable — and every correction is
kept in the Decision's Amendments rather than smoothed over. One part is still
open on purpose. The origin is not yet locked to Cloudflare, so the client
address the applications see can be trusted only as far as the connection
carrying it. The lock is planned as the last step of the rebuild, after real
traffic has been proven, because switching it on too early would break every
host at once.

## What is kept, and how it is proven

The claim the rebuild set out to make true is that this repository plus one
storage bucket is enough to reconstruct everything
([Decision 0003](../adr/0003-recoverability.md)). Everything declarative is in
git. Secrets are in git too, encrypted with SOPS and age, with the private key
held outside both the cluster and the repository — Sealed Secrets was rejected
because its key lives inside the cluster, which would make a total loss destroy
the means to decrypt its own backups ([secrets runbook](../runbook/secrets.md)).
The rest is data, and the only data git cannot rebuild is PostgreSQL.

PostgreSQL runs on CloudNativePG with its three instances on Hetzner Cloud
Volumes, which outlive any server and are now kept even when Kubernetes lets
go of them. Its write-ahead log is archived continuously to Cloudflare R2, and a
full base backup is taken every day. The bucket's lock covers the write-ahead
log only: the backup tool rewrites one file at the end of each base backup, and
a lock over everything made every base backup fail. That was found by reading
the tool's source and proven on the bucket before anything was built on it.

A backup that has never been restored is a hope, so a Restore Drill runs every
month. It restores the newest backup into a throw-away cluster inside the same
cluster, checks that it replayed the log up to the newest archived segment and
that every database holds data, reports the result, and deletes what it made.
The first drill passed on 2026-10-01, and a deliberately impossible time limit
proved that its failure is loud as well. Because a broken cluster cannot be
trusted to report on itself, a daily watcher outside it, on GitHub, checks that
backups and drills are fresh and that no volume has been left detached — an
Orphaned Volume, which after a total loss may hold the newest data of all. Both
are described in steps 16 and 17 of the cluster services runbook.

## How an application arrives

Two applications run today, Authos and doma; two older ones stay in the
repository without being deployed. Each application's own CI builds an image
and changes one line in this repository: the image tag in that component's
overlay. ArgoCD notices the change and rolls the component out. Nothing about
an Application is written by hand. One ApplicationSet generates every one of
them from the directory layout, and the name, the ArgoCD project and the
namespace all follow from the path, so none of them can drift from the others.

The generator lists its projects instead of sweeping every directory, so a
project that is kept but not deployed cannot be adopted by accident. Each
project's ArgoCD project may create nothing cluster-wide, which is why
namespaces are committed files applied once rather than something ArgoCD
creates. And deleting the ApplicationSet leaves the workloads running, a choice
made because this cluster was once lost to a single deletion.

When a new version is actually running and healthy, ArgoCD sends one signal,
and a workflow in this repository turns it into a catalog row, a deployment
record on the application's repository and a message. How that flow works, and
the ways it fails, is the subject of
[its own explanation](../../deployments/HOW-IT-WORKS.md).

## What is not built yet

Some of the rebuild is still ahead, and the [rebuild Plan](../runbook/rebuild-2026-09-20.md)
tracks it; the [Status view](/status/) shows its open steps. A recovery runbook
— the procedure for bringing the databases back from the bucket after a real
loss, as opposed to the drill's rehearsal — is designed and not yet written.
The origin lock waits for last. Monitoring was deliberately not repaired: its
old files pinned work to a Worker name that no longer exists, and its scope is to be decided before
any of it returns. The Control Plane is still single, and `kluster` has not yet
been brought in line with the runbooks it serves.

Each of these is a gap in a picture that is otherwise complete for the first
time: what the cluster runs is written down, what it stores is backed up, and
the backup is restored on a schedule by something that says when it fails.
This page describes that picture as of the date it carries; when the picture
changes enough, a new revision replaces it.
