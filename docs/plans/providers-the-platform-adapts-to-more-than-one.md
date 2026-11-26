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
its reliability (`high`, or `best-effort` without a data centre's power,
cooling and link), its reach (a public address or behind NAT), its block
volumes and its load balancer. A Hetzner site has volumes and a load balancer;
a self-hosted machine has neither and sits behind NAT, but it can still carry
workload. The profiles of every site live in the Node set file of each
environment, the Hetzner servers belong to the site whose provider is
`hcloud`, and kluster writes every
capability as an admin label under `tosak.net.node-restriction.kubernetes.io/`.
A kubelet cannot set a key under that prefix (NodeRestriction, read in the
kubernetes v1.37.0 source), so no node can claim a capability it lacks.

Placement follows the profile. Nodes of high reliability, on every provider,
carry no taint, so any workload may run there, and a new client app goes to any
of them. Best-effort nodes register with the taint
`tosak.net/reliability=best-effort:NoSchedule` and take only workloads that
tolerate it; Grafana and cert-manager are the
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
The control plane is never removed this way, a node with an attached volume
only once its provider reports it off, and no node while half or more of the
Workers are not Ready
([ADR 0016, Amendment 2026-11-24](../adr/0016-every-provider-is-a-set-of-capabilities-and-workers-of-any-provider-join-as-equals.md#amendments)). A removed machine that comes back
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
      runs against it: OVHcloud, with a self-hosted machine as the first node outside
      Hetzner, proven in a rehearsal and then on live.
- [x] Write the Decision page: the capability model, the mesh, the providerID
      and CCM and CSI scope, placement and dead-node removal
      ([ADR 0016](../adr/0016-every-provider-is-a-set-of-capabilities-and-workers-of-any-provider-join-as-equals.md)).
      It amends ADR 0006, because both Hetzner firewalls open UDP 51821.

## Part B: nodes of any provider join as equals

**Done when:** on live, the self-hosted machine joined through `kluster node add`
from behind NAT, with its profile labels and the best-effort taint, and a pod that
tolerates the taint reaches a Service and DNS on other nodes with full-size
packets; the hcloud CCM neither deletes the node nor sends load-balancer
traffic to it, and no hcloud CSI node pod runs on it; and the self-hosted node,
powered off, was removed after 20 minutes with its pods restarted elsewhere,
then brought back by `kluster node add`.

- [x] Capability profiles: the Node set file per environment holds the sites
      and their profiles (provider, reliability, reach, block volumes, load
      balancer), the Hetzner servers belong to the `hcloud` site, and kluster
      writes the admin labels and checks them after each join
      ([Set the node profile](../runbook/workers.md#set-the-node-profile)).
      Every live Node carries the `hel1` profile since 2026-11-17.
- [x] The mesh in kluster: `wg-mesh` on every node, the key store
      `kube-system/mesh-peers`, the relay, UDP 51821 on both Hetzner firewalls
      behind the switch `allow_mesh_udp`, off by default, and the join checks
      for an overlapping LAN and the link MTU
      ([Join the mesh](../runbook/workers.md#join-the-mesh)). A machine outside
      Hetzner joins once the bring-your-own step below exists. Merged on
      2026-11-18 and proven by unit tests and a WireGuard test in network
      namespaces; it has not run on a cluster.
- [x] The mesh catalog in Git: a CronJob sends the full `mesh-peers` store to a
      workflow that writes the branch `mesh-catalog`. The catalog holds no
      endpoints, and the workflow needs one new GitHub token that the operator
      creates ([nodes README](../../core/nodes/README.md)). Live since
      2026-11-19, proven there: a run on the empty store sent nothing, a fake
      entry in the store reached `peers.json` in one commit, and the run after
      its removal took it back.
- [x] Flannel to `Backend.MTU` 1400. It changes the pod network of every node,
      so it is proven in a rehearsal before live. `kluster node pod-mtu`
      rebuilds `flannel.1` on each node and restarts every workload
      ([Workers runbook](../runbook/workers.md#change-the-pod-mtu)). Proven in
      a rehearsal on 2026-11-19 and live since 2026-11-22: pods at 1350,
      don't-fragment pings and 4 MiB pulls between every pair of nodes before,
      between and after the two halves of the change.
- [x] Keep the Hetzner-only parts on Hetzner servers: the CSI node DaemonSet
      gets the affinity `instance.hetzner.cloud/provided-by In [cloud]`, and
      both StorageClasses are owned by this repository with
      `allowedTopologies: csi.hetzner.cloud/location In [hel1]`
      ([the CSI driver](../runbook/control-plane.md#8c-the-csi-driver)). Live
      since 2026-11-22: the classes changed in place, and a test claim got a
      volume whose PV requires `hel1`, which was deleted with its claim.
- [x] Bring-your-own nodes in kluster: the `byo` provisioner, the kubelet
      settings above, the best-effort taint at registration, the bootstrap with a
      console-checked host key, and a ready check that no longer waits for an
      `hcloud://` providerID
      ([Add a bring-your-own machine](../runbook/workers.md#add-a-bring-your-own-machine)).
      Each machine of the Node set's `machines` section names its provisioner;
      kluster reaches a byo machine by an SSH jump through the Control Plane
      once it has joined the mesh, and closes it with a site firewall that
      takes new connections only from `wg-mesh` and keeps its pods off the
      LAN. Flannel gets a second `--iface-regex` for `wg-mesh`, in Git only
      ([ADR 0016, Amendment 2026-11-23](../adr/0016-every-provider-is-a-set-of-capabilities-and-workers-of-any-provider-join-as-equals.md#amendments)).
      Built on 2026-11-23 and proven by unit tests, the site firewall in network
      namespaces and the jump against an SSH server in the test process; no
      byo machine has been built yet.
- [x] `node remove` and `node replace` for a byo machine: drain, delete the
      Node, `kubeadm reset` over the jump, its key out of `mesh-peers` and its
      entry out of the Node set, then the mesh without it
      ([Take a bring-your-own machine out](../runbook/workers.md#take-a-bring-your-own-machine-out)).
      `node remove` is one run and leaves the machine as after its install:
      the pod network's leftovers, the site firewall and `wg-mesh` with its
      key go too, so it can join another cluster without a reinstall.
      `--unreachable` takes out a machine kluster cannot log in to, and
      `node replace` rebuilds the machine in place
      ([ADR 0016, Amendment 2026-11-24](../adr/0016-every-provider-is-a-set-of-capabilities-and-workers-of-any-provider-join-as-equals.md#amendments)).
      Built on 2026-11-24 and proven by unit tests; no byo machine has left a
      cluster yet.
- [x] Placement: the required affinities on capabilities (PostgreSQL, Envoy,
      Stalwart), the preferred zone of the data for apps with PostgreSQL, and
      the best-effort toleration for Grafana and cert-manager
      ([ADR 0016, Amendment 2026-11-23](../adr/0016-every-provider-is-a-set-of-capabilities-and-workers-of-any-provider-join-as-equals.md#amendments)).
      Envoy and Stalwart require `load-balancer=hcloud` and PostgreSQL
      `block-volumes=hcloud`; `authos-api`, `doma` and Stalwart prefer the
      site of the PostgreSQL primary through the kustomize Component
      `core/placement/data-zone`; Grafana and cert-manager carry only the
      toleration. Live since 2026-11-23: each rule was read on the running
      pods after Argo CD synced the merge. The Cluster's restart, which also
      took the postgres container to 256 Mi, left the primary without writes
      for about 3 minutes; then it took a write, streamed to the replica and
      archived its next WAL segment. No Node carries the best-effort taint
      yet, so the rules move no pod until the rehearsal.
- [x] The dead-node remover: a small Go controller in the cluster that taints
      and deletes a node NotReady for 20 minutes, never the control plane.
      Built as `node-controller` in the kluster module, its image on GHCR,
      with the volume guard, the majority guard and the pause annotation of
      the grill of 2026-11-24; proven by unit tests, a k3d test in CI, an hour
      on live with no action, and a fake Node on live removed with its
      `NodeRemoved` notice.
- [x] The self-hosted machine, reinstalled as a clean Ubuntu Server. Decided on
      2026-11-24: Proxmox VE on the machine, and the Node one VM on it with
      Ubuntu Server 24.04 from the cloud image, made through a template and
      cloud-init ([Build a Node VM on a self-hosted site](../runbook/build-a-node-vm-on-a-self-hosted-site.md)).
      Ubuntu on the hardware itself was the other option; Proxmox costs about
      1.5 GiB of the machine's memory, and gives a remote console and
      snapshots, a rehearsal VM that is deleted and made again for live, and
      no reinstall when the [Elastic nodes Plan](./elastic-nodes-the-cluster-grows-and-shrinks-on-spare-compute.md)
      adds VMs. One VM, not several: several do not fit the memory beside the
      host without overcommit, and kluster refuses a second machine on a
      self-hosted site until it builds the LAN routes. Proxmox over Incus for
      its maturity and web UI. Proven on 2026-11-25 and 2026-11-26: one full
      memory-test pass, then 24 hours idle with no machine-check error and none
      of 84,377 pings to the gateway lost, and the four byo-check checks by hand
      in the VM `ts1-wk1`. The operator reaches the host and the VM from
      outside the site through Tailscale on the host.
- [ ] Rehearsal: `rehearsal.tfvars` moves off `cx23` to a type in stock, the
      self-hosted machine joins the rehearsal, and the three checks of the finish line
      pass there. The rehearsal's cost needs the operator's yes.
- [ ] Live: the self-hosted machine is reset, joins live, and the three checks pass again.
      First `core/cni/flannel.yaml` with its second `--iface-regex` is applied
      on live by hand (`core/cni` is not synced), which rolls the Flannel
      DaemonSet on every node.

## Part C: the platform runs with no Hetzner node

**Done when:** a rehearsal shows the platform serving its public hosts and its
data with every Hetzner node gone, and the control plane, the entry point, the
storage and the node network each run on other providers.

- [ ] The OVHcloud trial node: one VPS-1 (4 vCores, 8 GB, about 6.99 EUR a
      month before VAT with the backup option) in DE, else SBG, for one month.
      It is ordered only when the stock API shows it and the operator says yes
      at order time, and it joins as an equal node of high reliability with no taint.
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
  `wg-mesh`, and that a mesh address reached over a host route on a
  self-hosted site's LAN passes `rp_filter`.
- Where the LAN host routes between two machines of one self-hosted site are
  built: in a separate Plan for the self-hosted machine, which does not exist
  yet, or in the
  [Elastic nodes](./elastic-nodes-the-cluster-grows-and-shrinks-on-spare-compute.md)
  Plan. Until then the Node set refuses a second machine on such a site.
- The name of what tosak.net is. It runs on other providers' compute, so
  "cloud platform" may not fit; the operator has not chosen a name.
