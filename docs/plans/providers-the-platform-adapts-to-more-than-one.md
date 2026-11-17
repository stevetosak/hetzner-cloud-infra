---
title: "Providers: the platform adapts to more than one"
description: "Why tosak.net stops depending on any one provider, the capability model its design settled on, and the two parts that get there: nodes of any provider join as equals, then the services still bound to Hetzner become provider-neutral."
type: plan
status: active
topics: [provisioning, kubernetes, storage, networking]
---

Every node of the cluster runs on Hetzner Cloud, and several parts of the
platform assume it. The cost of that showed on 2026-11-08, when `cx23`, the
type the control plane and the workers were sized on, could no longer be
ordered, and again on 2026-11-16, when neither `cx23` nor `cx33` was in stock.
The second provider had gaps of its own: OVHcloud's stock API showed every
8 GB VPS plan out of stock at every site that day. A single provider can change
its offer at any time, and today the only answer is to accept what that
provider sells.

**The goal is that no provider is core: the platform keeps running with any
provider gone, Hetzner included.** What matters is room and options for the
current workload, wherever it runs, with the same behaviour on every provider.
The way there is hybrid first: nodes of a second provider and self-hosted
machines join the one cluster (part B), and then the services that still need
Hetzner become provider-neutral (part C). OVHcloud is the second provider,
in Limburg (DE) first and Strasbourg (SBG) second.

The parts bound to Hetzner today, read from the repository on 2026-11-08:

- **Compute and network**: the Terraform Modules under `infra/` use the
  `hcloud` provider for servers, the private network and the firewall, and
  kluster builds and proves the cluster against a Hetzner project
  ([ADR 0009](../adr/0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md)).
- **Storage**: the default StorageClass `hcloud-volumes` comes from the
  Hetzner CSI driver (`core/hcloud/`), and its volumes are bound to one
  location, so the PostgreSQL workers are too
  ([ADR 0005](../adr/0005-storage-and-worker-identity.md)).
- **Entry point**: the public Gateway and the mail server sit behind Hetzner
  load balancers, which reach only Hetzner servers
  ([ADR 0008](../adr/0008-gateway-api-and-the-public-entry-point.md)).
- **Control plane**: the one control-plane node is a Hetzner server.
- **Outside the cluster**: Cloudflare holds DNS and the DNS-01 challenge, and
  R2 holds the PostgreSQL backups and the Terraform state. These are separate
  dependencies with the same question.

## Every provider is a set of capabilities

The design treats the nodes of every provider as equal members of the cluster
and describes each site by what it can do. A site's profile names its provider,
its reliability (a data centre or a home), its reach (a public address or
behind NAT), its block volumes and its load balancer. A Hetzner site has
volumes and a load balancer; a home PC has neither and sits behind NAT, but it
can still carry workload. The profiles live per site in the Node set file of
each environment and in the Hetzner Worker set, and kluster writes every
capability as an admin label under `tosak.net.node-restriction.kubernetes.io/`.
A kubelet cannot set a key under that prefix (NodeRestriction, read in the
kubernetes v1.37.0 source), so no node can claim a capability it lacks.

Placement follows the profile. Data-centre nodes of every provider carry no
taint, so any workload may run there, and a new client app goes to any of
them. Home nodes register with the taint `tosak.net/reliability=home:NoSchedule`
and take only workloads that tolerate it; Grafana and cert-manager are the
first, because neither holds state. A workload that needs a capability has a
required affinity on it: PostgreSQL on the Hetzner volumes, Envoy and Stalwart
on the Hetzner load balancer, until part C removes those bindings. An app that
uses PostgreSQL prefers the zone of its data, because distance costs every
query: a ping from `k8s-cp` to OVH SBG took 40.5 ms (10 pings, 2026-11-16).

The nodes reach each other over a WireGuard mesh that kluster builds and
manages (`wg-mesh`, 10.101.0.0/16, UDP 51821), with Flannel's VXLAN layered
inside it at `Backend.MTU` 1400, so pods get 1350. Two sites behind NAT talk
through a relay on a named public node; a pair inside one network the operator
owns skips the mesh. The control plane is a mesh member, and the VPN `wg0`
stays for operator traffic only. A node outside Hetzner registers without
`--cloud-provider=external`, with the providerID `kluster://<site>/<node>`, its
mesh address as node IP and the label
`node.kubernetes.io/exclude-from-external-load-balancers`. The hcloud cloud
controller manager returns an error for that providerID prefix and leaves the
node in place (read in the hcloud CCM v1.37.0 source); the hcloud CSI node
binary exits without Hetzner's metadata service, so its DaemonSet gets an
affinity that keeps it on Hetzner servers.

Nodes come and go through one command set, `kluster node add|remove|list|replace`,
with a provisioner per provider: Terraform for Hetzner, and "bring your own"
for a machine the operator sets up. A bring-your-own machine is reached once at
its own address, with its host key checked against the provider's console, and
after that only through the hub. A dead node, NotReady for 20 minutes, is
removed automatically on any provider: a controller in the cluster gives it
the `node.kubernetes.io/out-of-service` taint, so StatefulSet pods such as the
Argo CD application controller restart elsewhere, and then deletes the Node.
The control plane is never removed this way. A removed machine that comes back
returns through `kluster node add`, because a running kubelet does not register
again after its Node is deleted (kubernetes v1.37.0 source). Growing and
shrinking the cluster by itself is a separate Plan,
[Elastic nodes](./elastic-nodes-the-cluster-grows-and-shrinks-on-spare-compute.md).

## Design

**Done when:** a Decision page records what "adapts to a provider" means for
tosak.net, which of the bound parts move behind an interface and which stay
provider-specific, and the order of the work.

- [x] Decide in a grill what the goal is: a provider-neutral cluster (no
      provider is core), reached through a hybrid one.
- [x] Decide in a grill, for each bound part above, whether it stays, gets a
      provider-neutral replacement, or gets one implementation per provider:
      every provider is a capability profile; the control plane, the entry
      point, the storage and the private network become provider-neutral in
      part C.
- [x] Decide which second provider proves the design, and how a rehearsal
      runs against it: OVHcloud, with the home PC as the first node outside
      Hetzner, proven in a rehearsal and then on live.
- [ ] Write the Decision page: the capability model, the mesh, the providerID
      and CCM and CSI scope, placement and dead-node removal. It amends
      ADR 0006, because both Hetzner firewalls open UDP 51821.

## Part B: nodes of any provider join as equals

**Done when:** on live, the home PC joined through `kluster node add` from
behind NAT, with its profile labels and the home taint, and a pod that
tolerates the taint reaches a Service and DNS on other nodes with full-size
packets; the hcloud CCM neither deletes the node nor sends load-balancer
traffic to it, and no hcloud CSI node pod runs on it; and the home node,
powered off, was removed after 20 minutes with its pods restarted elsewhere,
then brought back by `kluster node add`.

- [ ] Capability profiles: the Node set file per environment holds the sites
      and their profiles (provider, reliability, reach, block volumes, load
      balancer), the Hetzner Worker set gets a profile, and kluster writes the
      admin labels and checks them after each join.
- [ ] The mesh in kluster: `wg-mesh` on every node, the relay, both Hetzner
      firewalls open for UDP 51821, and the join checks for an overlapping LAN
      and the link MTU.
- [ ] Flannel to `Backend.MTU` 1400. It changes the pod network of every node,
      so it is proven in a rehearsal before live.
- [ ] Keep the Hetzner-only parts on Hetzner servers: the CSI node DaemonSet
      gets the affinity `instance.hetzner.cloud/provided-by In [cloud]`, and
      both StorageClasses are owned by this repository with
      `allowedTopologies: csi.hetzner.cloud/location In [hel1]`. The live
      StorageClasses are deleted and made again only with the operator's yes.
- [ ] Bring-your-own nodes in kluster: the `byo` provisioner, the kubelet
      settings above, the home taint at registration, the bootstrap with a
      console-checked host key, and a ready check that no longer waits for an
      `hcloud://` providerID.
- [ ] Placement: the required affinities on capabilities (PostgreSQL, Envoy,
      Stalwart), the preferred zone of the data for apps with PostgreSQL, and
      the home toleration for Grafana and cert-manager.
- [ ] The dead-node remover: a small Go controller in the cluster that taints
      and deletes a node NotReady for 20 minutes, never the control plane.
- [ ] The home PC (Ryzen 5 3600X, 16 GB) as a clean Ubuntu Server machine.
- [ ] Rehearsal: `rehearsal.tfvars` moves off `cx23` to a type in stock, the
      home PC joins the rehearsal, and the three checks of the finish line
      pass there. The rehearsal's cost needs the operator's yes.
- [ ] Live: the home PC is reset, joins live, and the three checks pass again.

## Part C: the platform runs with no Hetzner node

**Done when:** a rehearsal shows the platform serving its public hosts and its
data with every Hetzner node gone, and the control plane, the entry point, the
storage and the node network each run on other providers.

- [ ] The OVHcloud trial node: one VPS-1 (4 vCores, 8 GB, about 6.99 EUR a
      month before VAT with the backup option) in DE, else SBG, for one month.
      It is ordered only when the stock API shows it and the operator says yes
      at order time, and it joins as an equal data-centre node with no taint.
      At the month's end a report gives its Ready time outside planned work,
      its mesh and pod-network faults, its PostgreSQL latency and its price per
      8 GB against Hetzner's next option; the operator decides keep or cancel.
- [ ] Decide in a grill how the control plane survives a provider: three
      control-plane nodes over providers, which keeps a quorum, or a fast
      restore on another provider.
- [ ] Decide in a grill the entry point without a Hetzner load balancer.
- [ ] Decide in a grill the storage: PostgreSQL on node disks with its own
      replication across providers and the R2 backups. That brings back the
      risk class ADR 0005 removed, so it needs its own Decision page and a
      guard.
- [ ] Decide whether Hetzner nodes move their node path from the private
      network onto `wg-mesh`, so every provider uses the same node network.
- [ ] Rehearse the loss of every Hetzner node.

## Open questions

- OVHcloud's capability values (a private network, volumes, a load balancer for
  a VPS) are read from OVHcloud when its profile is written; none is assumed.
- Two mesh details are not yet proven and go into the rehearsal: that Flannel
  VXLAN bound to one interface sends to a VTEP the kernel routes through
  `wg-mesh`, and that a mesh address reached over a home LAN host route passes
  `rp_filter`.
- The name of what tosak.net is. It runs on other providers' compute, so
  "cloud platform" may not fit; the operator has not chosen a name.
