# 1. Cluster network plan

Date: 2026-09-20

## Status

Accepted

## Context

The cluster was rebuilt from zero after every server was destroyed. Pod and
service CIDRs are fixed at `kubeadm init` and cannot be changed afterwards
without rebuilding the cluster, so they had to be decided before the rebuild
rather than inherited.

The previous cluster's CNI was never recorded — no manifest, no script, and no
trace in git history, including deleted files. There was nothing to inherit.

Two constraints came out of auditing the surviving addresses:

- The WireGuard VPN is `10.100.0.0/24`. The kubeadm **default** service CIDR is
  `10.96.0.0/12`, which spans `10.96.0.0`–`10.111.255.255` and therefore
  contains the entire VPN subnet. Operators reach the API server at
  `10.100.0.1:6443` and reach private ingress at `10.100.0.2`. A Service
  allocated an address in `10.100.0.0/24` would make kube-proxy intercept
  traffic to the API server itself, on every node. The old cluster survived
  only because ClusterIPs are allocated from the bottom of the range and it
  never had enough Services to reach `10.100.x`.
- The Hetzner private network is `10.0.0.0/16`. Hetzner route destinations must
  fall inside a network's own range, so CCM-driven native pod routing would
  require re-planning that range to carry pod CIDRs.

The cluster has **no NetworkPolicy objects at all**, so CNI policy enforcement
buys nothing at the time of this decision.

## Decision

- **CNI: Flannel**, VXLAN backend, MTU 1400 (Hetzner private network is 1450;
  VXLAN overhead is 50).
- **Pod CIDR `10.244.0.0/16`.** Verified disjoint from the private network, the
  VPN, and the service CIDR.
- **Service CIDR `10.96.0.0/16`**, narrowed from the `/12` default so it ends at
  `10.96.255.255` and cannot collide with the VPN.
- **`10.245.0.0/16` is reserved and left unallocated**, solely to keep Cilium's
  supported per-node migration available. That migration runs Cilium alongside
  Flannel and requires the two to hold different pod CIDRs.
- **The CNI install is its own `pipeline.Stage`**, not inlined into the kubeadm
  stage, so swapping `FlannelStage` for `CiliumStage` is a change to one slice.
- Native pod routing via the Hetzner CCM route controller is **rejected for
  now**, not ruled out.

## Consequences

- Flannel enforces no NetworkPolicy. If policy is ever needed — plausible, as
  the cluster hosts an identity provider — that is the trigger to migrate to
  Cilium, and the reserved CIDR plus the pluggable stage make it a planned
  change rather than a rebuild.
- Narrowing the service CIDR caps the cluster at 65,534 Services. Irrelevant at
  this scale.
- `10.245.0.0/16` will sit unused, possibly for a long time, and will look like
  an oversight to anyone who has not read this record. That is the cost of
  keeping the option.
- Native routing stays available later, but claiming it means re-planning
  `10.0.0.0/16`, which is a larger change than swapping a CNI.
