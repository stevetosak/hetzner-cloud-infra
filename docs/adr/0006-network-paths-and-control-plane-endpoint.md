# 6. Network paths, and the control plane's name for itself

Date: 2026-09-20

## Status

Accepted. Extends ADR 0001, which chose the CIDRs and the CNI. This one says
which interface carries which traffic, and what holds that true.

Amended 2026-09-20, at the close of Phase 2: the single `allow_public_ssh`
variable named below is now two, `allow_public_ssh_cp` and
`allow_public_ssh_worker`. The two-firewall decision stands unchanged; only its
switch was split. One switch meant closing the Control Plane's bootstrap port
would also have closed every Worker's, so Phase 3 would have had to reopen the
Control Plane it no longer needs. Two firewalls that each say one thing were
the point, and they now have one control each.

## Context

Every server has three interfaces: a public NIC, `enp7s0` on the Private
Network `10.0.0.0/16`, and `wg0` on the VPN `10.100.0.0/24`. Cluster traffic
went over private addresses in the cluster that was lost — but that rule was
written down nowhere. It survived in operators' memory and in a handful of
flags scattered across scripts, which is the same way the previous cluster's
CNI choice survived: not at all (ADR 0001).

Auditing the rule turned up three things.

**Flannel was never pinned to an interface.** Left to choose, flannel takes the
first interface it finds, which is the public one. Whether the lost cluster
tunnelled pod traffic over public addresses is unknowable — no manifest
survives. Its MTU would have been wrong too, since the derived value follows
the chosen interface.

**Hetzner firewalls cannot filter the Private Network at all.** The four
subnets were created so that traffic could later be filtered by source subnet.
Hetzner's own FAQ answers this directly: "Can Firewalls secure traffic to my
private Hetzner Cloud Networks? Not yet, because we consider the private
networks to be 'secure'." A Hetzner firewall only ever sees the public
interface.

**`--control-plane-endpoint` was missing from the planned `kubeadm init`.**
Without it kubeadm writes a literal IP into every kubeconfig and into the
record joining nodes read, and a cluster built that way can never gain a second
control plane without being rebuilt. Setting it costs one argument. ADR 0002
assumes one control plane, and that assumption is sound — but there is a
difference between choosing one and making a second impossible.

## Decision

**Each interface has one job.**

- **Private Network** carries all cluster-internal traffic.
- **VPN** carries operator traffic only: the API server, private ingress, and
  SSH to any server. The Control Plane is its hub *and its router* — an
  operator reaches a Worker because the Control Plane forwards between two of
  its own peers. This is why every Worker holds a tunnel despite reaching the
  API server directly over the Private Network, and it is why port 22 stays
  closed on every public interface outside a bootstrap.
- **Public Interface** carries outbound traffic only, plus the WireGuard
  endpoint — the one port open to the world.

**Six settings hold that true. There is no seventh, and nothing enforces it.**

| # | Setting | Keeps private |
|---|---|---|
| 1 | `kubeadm init --apiserver-advertise-address=10.0.1.5` | where the API server listens |
| 2 | `kubelet --node-ip=<private>` | what a node advertises as itself |
| 3 | hcloud CCM with `HCLOUD_NETWORK` set | Node InternalIP |
| 4 | flannel `--iface-regex=^10\.0\.` | pod-to-pod VXLAN |
| 5 | `load-balancer.hetzner.cloud/use-private-ip: "true"` | load balancer to ingress |
| 6 | the Endpoint's `/etc/hosts` entry | joins, and every node kubeconfig |

Each is a default that points the wrong way if left alone. A failure in any of
them is silent: traffic still flows, over the wrong interface, at the wrong
MTU. Nothing reports it, because "private" here is an addressing convention,
not a boundary.

**The subnets stay, as addressing rather than isolation.** Role-based CIDRs let
a future rule be written as `10.0.1.0/24` instead of a list of addresses. If
filtering is ever wanted, the layer depends on what is being filtered:
node-to-node is host `nftables` on `enp7s0`, where the subnets pay off;
pod-to-pod is NetworkPolicy and therefore Cilium, where they do not, because
between nodes that traffic is VXLAN and a host firewall sees only UDP 8472
between node addresses. `10.0.3.0/24` is **reserved and empty** — ADR 0005 put
PostgreSQL in a pod on a Volume, so nothing will hold an address there unless a
core server outside Kubernetes is added.

**The cluster gets a name for its own API server: `k8s-cp.tosak.internal`.**
Passed as `--control-plane-endpoint`, resolved by each node from its own
`/etc/hosts` to `10.0.1.5`. Not in public DNS: `.internal` is reserved for
private use, publishing `10.0.1.5` to the world helps nobody who could use it,
and a cluster should not need an external resolver to start.

The operator does **not** resolve the name. A kubeconfig names
`https://10.100.0.1:6443` directly, so the operator route stays the VPN and the
Private Network invariant holds for everything else. Both work because both
addresses are in the certificate. `46.62.209.249` is in there too, purely as
break-glass: if WireGuard fails while the host is healthy, port 22 and 6443 can
be opened briefly to one address without re-issuing certificates first.

**Only the hub accepts an inbound WireGuard port.** The VPN is hub and spoke:
a Worker's `wg0` carries an `Endpoint` and no `ListenPort`, so it dials out
from an ephemeral port; the Control Plane carries a `ListenPort` and peers with
no `Endpoint`, so it learns each peer's address from the first packet to arrive
and `PersistentKeepalive` keeps that mapping fresh. A Worker therefore never
needs an inbound port.

`shared/` holds **two** firewalls rather than one. `tosak-cp-firewall` opens
UDP 51820 to the world; `tosak-worker-firewall` opens nothing, apart from
bootstrap SSH while `allow_public_ssh_worker` is set. One firewall covering
every
server would have exposed a world-reachable UDP port on four machines to serve
one. Hetzner firewalls are stateful, so a Worker with no inbound rule still
reaches apt and the image registries and the replies come back — which is what
makes keeping public addresses on Workers defensible: in steady state their
public interface accepts nothing.

Rejected: one firewall scoped with `apply_to { label_selector = ... }`. The
labels exist and it would work, but a single firewall whose rules mean
different things on different servers is harder to read than two firewalls that
each say one thing.

**A Worker's VPN address is declared, never derived.** `vpn_ip` sits in
`infra/workers/terraform.tfvars` beside `private_ip`, validated for uniqueness
and range. The previous bootstrap assigned VPN addresses by counting lines in a
generated file, so an address depended on a Worker's position rather than its
identity — the same defect as the deleted `node_suffix`, one layer up, and
silent in the same way, because the hub config was regenerated from the same
loop and stayed self-consistent while every external reference rotted.

**`tools/kluster/kluster.yaml` is committed.** It was the only record of the
VPN topology and it lived on one laptop. It holds public keys and addresses;
nothing in it is secret.

## Consequences

- Six settings must be re-checked after any upgrade that rewrites them —
  particularly the flannel manifest, where a straight re-download from upstream
  silently drops the interface pin and the MTU.
- `/etc/hosts` is per-host state, and per-host state nobody wrote down is how
  this cluster got into trouble. The bootstrap must write it on every node, and
  a Worker missing that line cannot reach the API server.
- The Control Plane is a single point of failure for **operator access**, not
  just for the API. It is the VPN hub and router, so while it is down there is
  no SSH to any Worker either. Recovery starts with
  `allow_public_ssh_worker = true` and a `terraform apply` in `shared/`.
- A second control plane stays possible: point the name at a load balancer on
  6443 inside `10.0.4.0/24`, then `kubeadm join --control-plane`. The
  certificates already trust the name.
- `10.0.3.0/24` sits empty and will look like an oversight to anyone who has
  not read this. That is the price of holding the range.
- Node-level filtering was not built, only kept reachable. Nothing filters the
  Private Network today.
