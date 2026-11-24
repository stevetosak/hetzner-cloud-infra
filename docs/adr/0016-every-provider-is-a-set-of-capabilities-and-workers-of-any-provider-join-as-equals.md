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
claim a capability it lacks, and a placement rule can trust the label. The
site label is also the zone a placement rule reads; no kubelet sets
`topology.kubernetes.io/zone` or `region`, because the Hetzner Nodes carry
neither.

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
public machine named in the Node set's mesh section, by static routes kluster
writes; a site that later gets a port forward becomes public, and its pairs go
direct.

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
the laptop over `wg0` to `k8s-cp`, then by an SSH jump over `wg-mesh`.

A pod packet between sites is VXLAN inside WireGuard, so packet sizes are
planned for the smallest link: the tunnel MTU is 1400, Flannel's
`Backend.MTU` goes from 1450 to 1400 for the whole cluster, and pods get 1350.
When a machine joins, kluster measures its site's link MTU and refuses a LAN
that overlaps a cluster range: the private network `10.0.0.0/16`, the pods'
`10.244.0.0/16`, the services' `10.96.0.0/16`, the VPN's `10.100.0.0/24` or the
mesh. On a self-hosted site, the host firewall lets pods reach the internet and the
cluster's machines, and no other address on the LAN. Every public machine
accepts UDP 51821: both Hetzner firewalls open it from any address once the
switch `allow_mesh_udp` is on, an OVHcloud machine's host firewall opens it, and
a self-hosted site behind NAT opens nothing. This changes ADR 0006, as its
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
owned by this repository in `core/hcloud/storageclasses.yaml`, next to the
driver and applied with it, with the chart's own switched off, and carry
`allowedTopologies: csi.hetzner.cloud/location In [hel1]`. The existing
PostgreSQL volumes already require `hel1`; the guard is for a new claim, which
with `WaitForFirstConsumer` and no allowed topologies can be provisioned for a
node without the driver
([external-provisioner#544](https://github.com/kubernetes-csi/external-provisioner/issues/544)).

### Workers come and go through one command set

Every Worker, on every provider, is added, removed, listed and replaced through
`kluster node add|remove|list|replace`. Each machine names its provisioner,
and the provisioner does that machine's part: `hcloud` creates the server with
Terraform as today, and `byo`, bring your own, takes a machine the operator
has set up. A provisioner is never read from a site's provider, because one
site can hold machines of two. The host Stages are shared; the kubelet flags,
the mesh peer and the Node check come from the provisioner. Every machine of
the mesh is listed in the `machines` section of the Node set file of its
environment, with its site, its provisioner and its mesh address; the first
`node add` run of a byo machine writes its entry and the operator commits it
before the second, the same rule as the Worker set. The repository is public,
so the file holds no address of a self-hosted site, and such a site's name,
`ts1` and on, tells nothing of where it is.

A bring-your-own machine is reached once at its own address, with the
cluster's key put in at install and its host key checked against the
fingerprint the operator reads on the machine's console; the host key is then
rotated. After that, kluster and the operator reach it only through the
Control Plane, by an SSH jump over `wg-mesh`, and the machine's own firewall
takes no new connection from anywhere but the mesh.

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
again in a rehearsal. Owning the StorageClasses changes the two live classes in
place, and that change needs the operator's yes.

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

**2026-11-18 — where the mesh keeps its keys, and what the first build leaves
out.** Building the mesh settled four details, and the text above is corrected
to match. The public keys live in one store, the ConfigMap
`kube-system/mesh-peers`, with one entry per machine: its name, site, mesh
address and public key. kluster reads a new key from the machine, writes the
store and pushes each machine's config over SSH, so no agent runs on a node, and
Argo CD does not manage the store. A node's own credentials cannot read a
ConfigMap in `kube-system`, which `kubectl auth can-i` showed on live on
2026-11-18, and that is a further reason the push comes from kluster.

The relay is named in the Node set's mesh section, next to the machines it
serves, and not in `kluster.yaml`. UDP 51821 stays closed on both Hetzner
firewalls behind the Terraform switch `allow_mesh_udp`, off by default, because
a Hetzner-only cluster has no pair that uses the mesh: two machines at one site
get no peer. The switch is turned on when the first machine outside Hetzner
joins.

The host routes between two machines on one self-hosted site's LAN are not
built yet. The design above keeps them, but only real hardware on a real LAN can
prove them, `rp_filter` included, so until then the Node set refuses a second
machine on a site whose provider is not `hcloud`. kluster also logs in only to
the Control Plane and the Worker set, so a machine outside Hetzner joins the
mesh once the bring-your-own step exists. Until then the NAT pair and the relay
are proven by the renderer's tests and by a WireGuard test in network
namespaces, not on a cluster.

**2026-11-19 — the store gets a public record.** The ConfigMap
`kube-system/mesh-peers` keeps no history, so the mesh catalog records it in
Git: `peers.json` on the branch `mesh-catalog`, with each machine's name, site,
mesh address and public key, and never an endpoint, because the repository is
public. A CronJob in the new namespace `nodes`, which will also hold the other
helpers that keep the nodes in order, compares the store with the catalog every
hour and sends the whole store to a workflow only when the two differ. A
CronJob was chosen over a send from kluster after each write because it also
records a hand edit of the store. It records states, not every change: a change
put back within the hour leaves no trace, and only a watch on the ConfigMap
would close that gap.

**2026-11-22 — the StorageClasses change in place, and both live in
`core/hcloud`.** The text above said an existing StorageClass's
`allowedTopologies` cannot be changed, so owning the classes meant deleting
and recreating the live ones. That is wrong for this cluster: a server-side
dry run of `allowedTopologies` on both live classes (`kubectl diff` and
`kubectl apply --dry-run=server`, Kubernetes v1.37.0) passed validation. Only
`provisioner`, `parameters`, `reclaimPolicy` and `volumeBindingMode` cannot
change. The text above is corrected to match. Both classes live in
`core/hcloud/storageclasses.yaml`, which `kluster cp init` applies after the
driver, and `hcloud-volumes-retain` leaves `core/cnpg`, so Argo CD no longer
manages it. The `csi` Stage refuses a class file whose location is not the
name of the `hcloud` site in the environment's Node set, and it only applies:
no kluster run deletes a live class.

**2026-11-23 — building the bring-your-own provisioner.** Building `byo`
settled six details, and the text above is corrected to match.

Every machine names its provisioner. The first build took "a machine of the
mesh on a site that is not `hcloud`" to mean `byo`, which holds only while one
other provisioner exists; an `ovh` provisioner in part C, or virtual machines on
the self-hosted machine in the
[Elastic nodes](../plans/elastic-nodes-the-cluster-grows-and-shrinks-on-spare-compute.md)
Plan, would break it, and one site can hold machines of two provisioners. The
Node set therefore holds one top-level `machines` section, each entry with its
site, its provisioner and its mesh address, and the `mesh` section keeps only
the relay. kluster refuses an unknown provisioner, an `hcloud` machine off the
`hcloud` site, and any other provisioner on it.

kluster reaches a byo machine after its first login by an SSH jump through the
Control Plane, not by a routed path. The grill of 2026-11-17 had chosen to give
the laptop's VPN peer the mesh range and make `k8s-cp` forward between `wg0` and
`wg-mesh`, with a route back to the VPN on every machine outside Hetzner. That
cannot work in a rehearsal, whose VPN addresses are the live ones that the
laptop routes to the live hub. The jump is one path for both environments,
needs no change on the laptop and none in the mesh, which keeps carrying node
addresses only. It depends on TCP forwarding in the Control Plane's sshd, which
`sshd -T` showed on, with `permitopen any`, on live on 2026-11-23.

The bootstrap address and the host key's fingerprint are flags of the second
`node add` run, not entries in the file: both are read on the machine's console
at that moment, both are used once, and a LAN address in a public file only
goes stale. The machine is installed with the user every Worker has, `tosak`,
with the cluster's key and sudo without a password.

The machine's own firewall is wider than closing port 22. A Node listens on
every address on the kubelet's 10250, kube-proxy's 10256, node-exporter's 9100
and the NodePorts (`ss -ltn` on `k8s-cp`, 2026-11-23), which every device on a
home LAN could reach. An nftables table of its own, loaded at boot, takes new
connections only from `wg-mesh`, loopback and the machine's pods, and keeps
pods from every private address range but the cluster's. A test in network
namespaces proves both. The byo kubelet sets no topology labels: the Hetzner
Nodes carry no `topology.kubernetes.io/zone` or `region` (read on live the same
day), so a rule on those labels would see only byo machines, and the admin site
label already serves as the zone.

A byo machine on a site with a public address is refused for now. Its peers
would need that address, and neither the Node set nor the mesh store holds
one; the OVHcloud node of part C decides where it lives. Flannel picks a
machine's address with a second `--iface-regex=^10\.101\.` after `^10\.0\.`,
as the design said; Flannel tries the patterns in order (v0.28.9 source), so a
Hetzner server keeps its private address. The join check also refuses a LAN
inside `10.245.0.0/16`, which ADR 0001 keeps for a move of the pods to Cilium.

**2026-11-23 — placement as built.** Most of the binding already held without a
rule. Every pod with a Hetzner Volume is bound to `hel1` by the volume's own
topology, which only a Node running the Hetzner CSI node plugin carries, and
that plugin runs only on Hetzner servers. Every pod without the best-effort
toleration stays off a best-effort Node. Flannel, kube-proxy and node-exporter
already tolerate every `NoSchedule` taint, so they run on a byo machine as on
any other Node.

The required rules are therefore the ones the decision names. Envoy, through
its `EnvoyProxy`, and Stalwart need `load-balancer=hcloud`, because both sit
behind a Hetzner load balancer with `externalTrafficPolicy: Local`.
PostgreSQL's Cluster needs `block-volumes=hcloud`, beside CloudNativePG's
default anti-affinity. The volume topology already gave PostgreSQL that
binding; the rule states it where the Cluster is read. Applying it restarts
every instance, and the operator chose to take that restart now. The postgres
container's memory change from 384 to 256 Mi, which the Capacity Plan had held
for a later restart, goes in the same one.

The zone of the data is a preferred pod affinity to the pod that holds
CloudNativePG's primary role (`cnpg.io/instanceRole=primary` in `pg-cluster`),
with the admin site label as the topology key. A preference on the site name
would have to change in every application when the data moves; this one
follows the primary to any Node or site, and no application names a site. It
lives once, as the kustomize Component `core/placement/data-zone`, which an
overlay takes with `components:`. `authos-api`, `doma` and Stalwart take it
today, and the Base layer chart can emit the same term for any component that
uses the platform's PostgreSQL. Like every preference, it acts only when a pod
is scheduled: after a failover, a running pod stays where it is.

Grafana and cert-manager carry only the toleration, with no preference for
either reliability. The scheduler's own scoring places them, so a Node of high
reliability and a best-effort one stay equal choices, and an empty self-hosted
machine will most likely take them at their next restart.

**2026-11-24 — a bring-your-own machine leaves in one run, and `replace`
rebuilds it in place.** Building `node remove` and `node replace` for `byo`
settled three details. A byo machine has no server to delete and no Terraform
state, so `node remove` takes it out in one run instead of the Hetzner Worker's
two, and writes the Node set last for the operator to commit. It leaves the
machine as it was after its install, not only reset: after `kubeadm reset` it
removes the pod network's files and interfaces, the site firewall, and
`wg-mesh` with its key, so the machine takes SSH at its own address again and
moves from a rehearsal to live with `node add` and no reinstall. `wg-mesh`
goes last, from a timer, because the session to the machine runs over it, and
kluster proves it gone by a dial through the Control Plane that fails.

A machine kluster cannot log in to, dead, powered off or reinstalled, is taken
out with `--unreachable`: its Node is cordoned and deleted without a drain, and
its key, entry and pin go, while a Ready Node is refused because its kubelet
still answers. `node replace` cannot make a new byo machine as it makes a new
Hetzner server, so it rebuilds the machine in place, with its entry, mesh key,
site firewall and pins kept, and a reinstalled machine goes through
`node remove --unreachable` and `node add`. All three are the operator's
choices of 2026-11-24, and unit tests prove the order, the gates and the
refusals; no byo machine has left a cluster yet.
