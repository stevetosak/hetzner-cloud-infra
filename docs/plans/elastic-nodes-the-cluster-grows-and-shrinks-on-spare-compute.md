---
title: "Elastic nodes: the cluster grows and shrinks on spare compute"
description: "How the cluster adds and removes worker nodes by itself, first as virtual machines on a self-hosted machine, while it keeps room for every workload, at least three Ready workers and workers on two providers."
type: plan
status: proposed
topics: [provisioning, kubernetes]
---

Today a node joins the cluster only when the operator runs `kluster node add`.
On 2026-11-17 the operator set the direction for more: node handling may be
automatic, so the cluster takes more nodes when its workloads need them and
gives them back when they do not. The first spare compute is the self-hosted
machine of Providers part B: split into virtual machines, it can add nodes at
no cost, and they go again when the room is not needed. This is the start of
horizontal scaling, next to the per-pod sizing of the
[Autoscaling Plan](./autoscaling-workloads-grow-without-a-manual-resize.md).

The work comes after part B of the
[Providers Plan](./providers-the-platform-adapts-to-more-than-one.md), which
lets a node of any provider join and builds the controller that removes a dead
node. It builds on both: an elastic VM joins as a best-effort node, with
the best-effort taint, and the same controller binary gains the scaling.

**Automation never takes the cluster below its floor.** A healthy worker is
removed only when the other Ready workers still hold every pod's requests, at
least three Ready workers stay, and Ready workers stay on at least two
providers. Three is the floor because high availability with a majority
election needs it; the cluster does not run that way today only for lack of
memory. The control plane is never removed by automation. A node group that
costs money (Hetzner, OVHcloud) stays at a maximum size of 0 until the operator
writes the criteria under which automation may buy; VMs on the self-hosted machine
cost nothing and come first.

The starting design is cluster-autoscaler 1.37.0 (released 2026-11-06 for
kubernetes v1.37) with its `externalgrpc` cloud provider. The autoscaler makes
the decisions: it adds a node when a pod cannot be scheduled and a new node
would take it, checked with the scheduler's own logic, and it removes a node
that has been unneeded for 10 minutes or unready for 20 (its `FAQ.md` at the
1.37.0 tag). It touches only nodes that belong to a node group. An own gRPC
server, in the binary of the dead-node remover, answers its calls: it lists
the groups, creates a VM, and on a delete taints the node, deletes it, removes
its mesh peer and destroys the VM. The floor lives in that server, which
refuses a delete that would break it.

## Elastic nodes on the self-hosted machine

**Done when:** on live, a pod that tolerates the best-effort taint and fits
nowhere makes a new VM on the self-hosted machine join within 15 minutes (the autoscaler's
`max-node-provision-time`), the VM is removed once it is no longer needed, and
a delete that would break the floor is refused.

- [ ] Decide in a grill the hypervisor on the self-hosted machine (KVM with
      libvirt, or another), the VM image, how much of its memory the host
      keeps, and how
      the gRPC server reaches the hypervisor over the mesh.
- [ ] The VM image: Ubuntu Server that joins through the bring-your-own node
      steps, with the best-effort taint and a node group label.
- [ ] The gRPC server: the `spare-vm` group (minimum 0, maximum set by the
      machine's memory), create and destroy, the template node the autoscaler plans
      with, and the floor check.
- [ ] cluster-autoscaler 1.37.0 in `core/`, on nodes of high reliability, sized by
      measurement once it runs.
- [ ] Prove scale up, scale down and a refused delete in a rehearsal, then on
      live.

## Open questions

- In Providers part B the self-hosted machine is a node itself. Whether it stays one, or
  becomes only the host of the VMs, is part of the hypervisor grill.
- VMs on the self-hosted machine go down when it restarts. Whether they start again by
  themselves or the autoscaler replaces them is open.
- Several VMs on the self-hosted machine are several mesh machines on one site.
  The Node set refuses that until kluster builds the LAN host routes between
  them, and real hardware has to prove those routes, `rp_filter` included. They
  are built here unless a separate Plan for the self-hosted machine takes them
  first.
- The autoscaler adds a Deployment to a cluster whose worker memory requests
  were at 75 % on 2026-11-16; its own request is measured before it moves to
  live.
- Several VMs on the self-hosted machine fail together when it loses power or
  its link. The dead-node controller acts only while more than half of the
  Workers are Ready, so a site that holds half of them stops it, which fails
  safe but leaves the VMs' Nodes in place
  ([ADR 0016, Amendment 2026-11-24](../adr/0016-every-provider-is-a-set-of-capabilities-and-workers-of-any-provider-join-as-equals.md#amendments)).
  A label for a VM's host, a count per site that never covers the Control
  Plane's own site, and the hypervisor's report of a VM that is off as a power
  signal are the candidates.
- An agent on every node could restart a kubelet whose Node was deleted, fence
  a machine cut off from the Control Plane and its peers, and pull the mesh
  configuration instead of kluster's push over SSH. It acts on a node's own
  credentials, so it must never be able to write the profile labels. Whether
  it comes with this Plan, as a DaemonSet or a systemd unit, needs an ADR 0016
  Amendment.
