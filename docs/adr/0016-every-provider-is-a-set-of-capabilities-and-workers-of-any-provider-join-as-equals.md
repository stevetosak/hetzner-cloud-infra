---
title: "Every provider is a set of capabilities, and Workers of any provider join as equals"
description: "Why the cluster stops treating any provider as its core, how a site's capabilities decide where workloads run, and the mesh, registration and lifecycle rules that let a machine of any provider, a self-hosted one included, join as an equal Worker."
type: decision
status: accepted
date: 2026-11-17
topics: [networking, kubernetes, provisioning, storage]
---

Every server of the cluster runs on Hetzner Cloud in `hel1`, and several parts
of the platform assume it: the private network that carries node traffic
([ADR 0006](./0006-network-paths-and-control-plane-endpoint.md)), the
`hcloud-volumes` StorageClass, whose volumes attach only to Hetzner servers in
one location ([ADR 0005](./0005-storage-and-worker-identity.md)), the two
Hetzner load balancers in front of the Gateway and the mail server
([ADR 0008](./0008-gateway-api-and-the-public-entry-point.md)), the hcloud
cloud controller manager, and the Terraform Modules that create every server.
That held while Hetzner sold what the cluster needed. On 2026-11-08 `cx23`, the
type every server was sized on, could no longer be ordered; on 2026-11-16
neither `cx23` nor `cx33` was in stock, and the next shared type, `cpx32`, cost
more than twice as much per gigabyte of memory. The second provider had gaps
too: OVHcloud's stock API showed every 8 GB VPS plan out of stock at every site
that day.

The operator set the goal on 2026-11-17: no provider is core. The platform
keeps running with any provider gone, Hetzner included, and what matters is
room and options for the current workload wherever it runs, with the same
behaviour everywhere. The same week brought a second kind of machine: any
Ubuntu machine may join, self-hosted ones included, starting with one
self-hosted machine behind a router's NAT. Providers are not equal, though. A
data centre has power, cooling and a link that a self-hosted site may lack;
Hetzner offers volumes and a load balancer that the self-hosted machine lacks;
and distance costs: a ping from `k8s-cp` to OVHcloud's Strasbourg site took 40.5 ms
(10 pings, 2026-11-16).

The choice is hard to reverse because it reaches every layer at once: how a
machine joins, which network the nodes speak over, which controllers may touch
a Node, and where each workload may run. The steps and their order are in the
[Providers Plan](../plans/providers-the-platform-adapts-to-more-than-one.md);
this page records what was chosen and why.

## The decision

**Every site is described by a capability profile, Workers of every provider
join the one cluster as equal members, and workloads are placed by what a site
can do, not by who sells it.** The way there is hybrid first. In part B of the
Plan, machines outside Hetzner join beside the Hetzner servers, and what only
Hetzner offers, the volumes, the load balancers and the controllers that serve
them, stays as Hetzner capabilities. In part C, the control plane, the entry
point, the storage and the node network become provider-neutral, and the
platform is proven with no Hetzner server at all. OVHcloud is the second
provider, in Limburg (DE) first and Strasbourg second; the self-hosted machine is the
first machine outside Hetzner.

### A site's capabilities are labels no kubelet can set

A site is a place where machines run: a Hetzner location, an OVHcloud site, a
self-hosted site. Its profile names the provider, the reliability (`high` for a
site with a data centre's power, cooling and link, `best-effort` for one
without), the reach (`public`, or `nat` for a site no peer can send to), and the
kind of block volumes and of load balancer it has, if any. Hetzner's `hel1` has
high reliability, is public, and has Hetzner's volumes and load balancer; the
self-hosted machine's site is best-effort, sits behind NAT and has neither. The
profiles of every site of an environment are written in its Node set file, and
the Hetzner servers belong to the site whose provider is `hcloud`, so a new
provider or site is a new profile with no code change, and a self-hosted site
that gets a port forward changes only its reach.

kluster writes each capability onto every Node of the site as an admin label
under `tosak.net.node-restriction.kubernetes.io/`, beside the provider and the
site. A capability's label names its kind, `block-volumes=hcloud` or
`load-balancer=hcloud`, and a site without the capability has no label, so a
workload that needs Hetzner's load balancer never lands behind another one. A
kubelet cannot set or change a key under a
`node-restriction.kubernetes.io/` prefix: the kubelet's own flag check refuses
it at registration, and the NodeRestriction admission plugin refuses it on
update (read in the kubernetes v1.37.0 source). A machine therefore cannot
claim a capability it lacks, and a placement rule can trust the label. A
kubelet outside Hetzner sets `topology.kubernetes.io/zone` and `region` to its
site, which it is allowed to do.

### Placement follows the profile

Nodes of high reliability, on every provider, carry no taint, so any workload
may run on them, and a new client application goes to any of them. Best-effort
nodes register with the taint `tosak.net/reliability=best-effort:NoSchedule`
and take only workloads that tolerate it. A kubelet cannot change its own taints after registration,
but NodeRestriction checks taints only on update, so `--register-with-taints`
sets it at registration; kluster checks it after the join and adds it as admin
when it is missing. Grafana and cert-manager tolerate it first, because neither
holds state. Argo CD stays on nodes of high reliability.

A workload that needs a capability carries a required node affinity on its
label: PostgreSQL on block volumes, Envoy and Stalwart on the Hetzner load
balancer. These bindings last until part C makes the storage and the entry
point provider-neutral. Distance is a preference, not a rule: an application
that uses PostgreSQL prefers the zone of its data, because every query pays the
round trip, and services that need availability prefer nodes of high
reliability.

### The mesh is kluster's own, and Flannel runs inside it

Machines at different sites reach each other over `wg-mesh`, a WireGuard mesh
that kluster builds and manages: the range `10.101.0.0/16` with a `/32` per
machine, on UDP port 51821. Each machine makes its own key, and only the public
key leaves it. kluster writes `/etc/wireguard/wg-mesh.conf` and applies a
change with `wg syncconf`, so live tunnels stay up, and `wg-quick@wg-mesh`
brings the interface up at boot. Every private network the operator owns is
trusted, the Hetzner private network and each self-hosted site's LAN alike,
and a pair of
machines inside one trusted network talks over it directly; every other pair
uses the mesh. A site's reach decides how a pair connects. A machine behind NAT
starts its tunnels to public peers and keeps them open with a keepalive every
25 seconds. Two NAT machines at different sites talk through a relay, a
Hetzner Worker named in `kluster.yaml`, by static routes kluster writes; a site
that later gets a port forward becomes public, and its pairs go direct.

The mesh carries node addresses only. Flannel's VXLAN stays the one component
that routes pod traffic, and for a pair at two sites it runs inside the mesh.
Hetzner servers keep their private `10.0.x.x` address as node IP and VXLAN
endpoint. A machine outside Hetzner uses its mesh address for both, picked by a
second Flannel `--iface-regex=^10\.101\.` after `^10\.0\.`, and reaches a
Hetzner server's private address through the mesh. That is why the Control
Plane Endpoint `k8s-cp.tosak.internal` still resolves to `10.0.1.5` on every
machine, and the API server's certificate does not change. The Control Plane is
a mesh member like any other. The VPN `wg0` keeps its one job, operator
traffic, and the operator reaches a machine outside Hetzner through it: from
the laptop over `wg0` to `k8s-cp`, then over `wg-mesh`.

A pod packet between sites is VXLAN inside WireGuard, so packet sizes are
planned for the smallest link: the tunnel MTU is 1400, Flannel's
`Backend.MTU` goes from 1450 to 1400 for the whole cluster, and pods get 1350.
When a machine joins, kluster measures its site's link MTU and refuses a LAN
that overlaps a cluster range: the private network `10.0.0.0/16`, the pods'
`10.244.0.0/16`, the services' `10.96.0.0/16`, the VPN's `10.100.0.0/24` or the
mesh. On a self-hosted site, the host firewall lets pods reach the internet and the
cluster's machines, and no other address on the LAN. Every public machine
accepts UDP 51821: both Hetzner firewalls open it from any address, an
OVHcloud machine's host firewall opens it, and a self-hosted site behind NAT opens
nothing. This changes ADR 0006, as its
[amendment of 2026-11-17](./0006-network-paths-and-control-plane-endpoint.md#amendments)
records.

### A Worker outside Hetzner is invisible to the Hetzner controllers

A Worker outside Hetzner registers without `--cloud-provider=external`, with
the providerID `kluster://<site>/<node>`, its mesh address as node IP and the
label `node.kubernetes.io/exclude-from-external-load-balancers`. The hcloud
cloud controller manager stays for the Hetzner servers. The providerID is what
protects the Node, read in the source of the live versions (hcloud CCM v1.37.0,
k8s.io/cloud-provider v0.36.4, kubelet v1.37.0). A Node with no providerID is
looked up by its name, reported as not existing, and deleted by the CCM's
lifecycle controller once it is NotReady. An unknown providerID prefix makes
the CCM's server lookup return an error instead, so the Node is not deleted,
and the load-balancer code skips it with a Warning event. Without a cloud
provider, the kubelet sets the providerID from its own configuration and adds
no taint that waits for a cloud controller.

Storage stays on Hetzner servers the same way. The hcloud CSI node binary exits
when Hetzner's metadata service gives it no server ID (CSI v2.23.0), so its pod
would crash in a loop on any other machine; the DaemonSet gets the affinity
`instance.hetzner.cloud/provided-by In [cloud]`. Both StorageClasses become
owned by this repository, with the chart's own switched off, and carry
`allowedTopologies: csi.hetzner.cloud/location In [hel1]`. The existing
PostgreSQL volumes already require `hel1`; the guard is for a new claim, which
with `WaitForFirstConsumer` and no allowed topologies can be provisioned for a
node without the driver
([external-provisioner#544](https://github.com/kubernetes-csi/external-provisioner/issues/544)).

### Workers come and go through one command set

Every Worker, on every provider, is added, removed, listed and replaced through
`kluster node add|remove|list|replace`. Each entry names its provider, and a
provisioner per provider does that provider's part: `hcloud` creates the server
with Terraform as today, and `byo`, bring your own, takes a machine the
operator has set up. The host Stages are shared; the kubelet flags, the mesh
peer and the Node check come from the provider. Machines that Terraform does
not create are listed in a Node set file per environment, which the first
`node add` run writes and the operator commits before the second, the same rule
as the Worker set. The repository is public, so the file holds no public
address of a self-hosted site, and such a site's name, `ts1` and on, tells
nothing of where it is.

A bring-your-own machine is reached once at its own address, with the
cluster's key put in at install and its host key checked against the
fingerprint the operator reads on the provider's console; the host key is then
rotated. After that, kluster and the operator reach it only through the hub,
and its public port 22 closes.

A dead Worker is removed on any provider. After 20 minutes NotReady or
Unknown, it gets the `node.kubernetes.io/out-of-service` taint, so its pods are
force-deleted and StatefulSet pods such as the Argo CD application controller
start elsewhere instead of waiting on a node that does not answer; then its
Node object is deleted. A small Go controller in the cluster does this, so it
acts when nobody runs kluster, and it never removes the Control Plane. A
bring-your-own machine keeps its mesh peer and comes back through
`kluster node add`, because a running kubelet does not register again after
its Node is deleted: nothing resets its registration flag (kubernetes v1.37.0
source). Growing and shrinking the cluster by itself, with a floor of three
Ready Workers on at least two providers, is a separate Plan,
[Elastic nodes](../plans/elastic-nodes-the-cluster-grows-and-shrinks-on-spare-compute.md).

## Why this and not the alternatives

Staying on Hetzner and paying for the next type in stock would have fixed one
month: the same gap can open at any provider, and nothing would change for the
next purchase. Going straight to a provider-neutral cluster would have needed a
rebuild before any new capacity arrived; part B adds capacity without one and
builds what part C needs anyway, the mesh, the bring-your-own provisioner and
the controller scope. The design first had Hetzner as a core set, with fixed
Hetzner Workers, machines of other providers added around them under a taint,
and new applications on Hetzner by default. It was dropped on 2026-11-17: with
Hetzner out of stock, a core set tied to one provider is the dependency the
Plan exists to remove, and the platform must not die if no Hetzner server can
be bought.

For the mesh, Flannel's own WireGuard backend (v0.28.10) has one endpoint per
node, so pod traffic between two Hetzner servers would have left the private
network for public addresses. Kilo (0.7.0) on Flannel adds a DaemonSet and
sends traffic between sites through one leader per location. NetBird or
Headscale bring a coordinator, one more control service to run and secure, and
Cilium's WireGuard still needs node addresses that every provider can reach.
kluster already renders WireGuard configuration from declared addresses, and
its own mesh can be designed as the cluster needs. Putting every pair on the
mesh, trusted networks included, stays possible later as the same rule with no
trusted network. Direct routing, with the pod ranges in the mesh's allowed
addresses instead of VXLAN, stays possible too; it was set aside so that
Flannel remains the only component that routes pods. Between two NAT sites, a
relay works today, with nothing to configure at the router; hole punching or a
coordinator would add code or a service for a case one self-hosted site does
not have.

Placement could have been opt-in by provider, as the first draft had it: a
taint per provider, and tolerations on whatever may leave Hetzner. That makes
Hetzner the default place of everything, a core set by another name. A taint
on best-effort sites only keeps every node of high reliability equal and leaves
reliability, the one real difference, as the only thing a workload opts into. Profiles could
have been one per provider in kluster's code, or one per node; one per site
lets a new provider arrive without code and lets two self-hosted sites
differ.

Removing dead Workers could have stayed with the operator, but a node that dies
at night would then hold its StatefulSet pods until someone looked, and kluster
runs only when the operator runs it. cluster-autoscaler (1.37.0) removes an
unready node only inside a node group it manages; it arrives with the Elastic
nodes Plan, while a small controller covers every Worker now.

## What it costs

The protection of a Worker outside Hetzner rests on an error path of the hcloud
CCM, not on a documented feature, so each CCM version bump has to prove it
again in a rehearsal. Owning the StorageClasses means deleting and recreating
the live ones, because an existing StorageClass's `allowedTopologies` cannot
be changed; that needs the operator's yes.

The pod MTU change touches every node: Flannel and every pod restart with the
new value. Each Hetzner server gets a world-reachable UDP port, where ADR 0006
had one port on one server; WireGuard sends nothing back to a packet without a
valid handshake from a known key, which keeps that exposure small but not
zero. Two mesh details are not proven yet and go into the rehearsal: that
Flannel's VXLAN, bound to one interface, sends to an endpoint the kernel routes
through `wg-mesh`, and that a mesh address reached over a host route on a
self-hosted site's LAN passes `rp_filter`. Traffic between two NAT sites goes through one relay
Worker, which adds its latency and bandwidth to those pairs and fails with it.

🔴 Kubernetes documents the `out-of-service` taint for a node that is already
shut down or powered off, and says to verify that before the taint is added
([non-graceful node shutdown](https://kubernetes.io/docs/concepts/cluster-administration/node-shutdown/),
read 2026-11-17). The dead-node controller cannot verify it. A Worker cut off
from the Control Plane while it still runs, a self-hosted site whose link drops for
example, gets the taint after 20 minutes like a dead one: its pods are
force-deleted and their volumes detached while the machine may still be
writing, and a StatefulSet pod can run twice until the link returns. How the
controller guards that case is decided when it is built in part B.

## Amendments

**2026-11-17 — the profile's values, and where the Hetzner profile lives.**
Building the profiles, the first step of part B, settled three details, and the
text above is corrected to match. The reliability values are `high` and
`best-effort`: a value names the guarantee a site gives, not the kind of place
it is, and a best-effort node registers with the taint
`tosak.net/reliability=best-effort:NoSchedule`. A capability's label names its
kind, `block-volumes=hcloud` and `load-balancer=hcloud`, and a site without the
capability carries no label, because Envoy and Stalwart need Hetzner's load
balancer and not any load balancer. The profile of every site, Hetzner's
included, lives in the environment's Node set file, and the Hetzner Worker set
stays as Terraform reads it: the Hetzner servers belong to the one site whose
provider is `hcloud`, and kluster checks that each server's location, read from
the Hetzner API, is that site's name. A Worker set entry names its own site only
once a second Hetzner location is in use. Self-hosted sites are named `ts1`,
`ts2` and on.
