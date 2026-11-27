---
title: "A self-hosted node joined the rehearsal from behind NAT and passed the three checks"
description: "The Providers Plan's rehearsal of 2026-11-26: the rehearsal moved to cpx22 and built the mesh, the VM ts1-wk1 joined it over the site's Tailscale route, passed the three checks of Part B's finish line, and showed that its Node does not come back by itself after a reboot."
type: log
date: 2026-11-27
topics: [provisioning, kubernetes, networking]
---

Part B of the [Providers Plan](../plans/providers-the-platform-adapts-to-more-than-one.md)
ends when the self-hosted machine joins live and passes three checks. Before
live, the rehearsal had to show the whole path once: the first byo join, the
first mesh on a real cluster, and the first removal of a dead Node that is not
a Hetzner server. It ran from 2026-11-26 22:53 to 2026-11-27 00:11 UTC and
cost about €0.16.

## The rehearsal ran on cpx22 with the mesh and node-controller

The rehearsal Worker set moved from cx23 to cpx22, which the Hetzner API showed
in stock in hel1 at 22:51 UTC (€0.0264 an hour before VAT, against €0.0088 for
cx23). The CX series is not expected back in stock, so every rehearsal now uses
cpx22; the Control Plane's type is still a variable default, so its cpx22 came
from a commit on the branch `rehearsal` only, beside `allow_mesh_udp = true`,
which must not reach live before the live join.

Three things the rehearsal never had before were added on `master`. The
rehearsal Node set declares the four Hetzner machines with mesh addresses in
`10.101.0.0/24` and the site `ts1` with `10.101.1.0/24`, so `up` builds the
mesh. A rehearsal cut, `core/rehearsal/nodes`, runs node-controller without
the CronJob that writes the public mesh catalog; the Secret Catalog marks the
catalog's token `rehearsal: omit`, and the Stage `rehearsal-cuts` now proves
both absent, 15 links in all. `kluster up --db fresh --apply` then built the
cluster: every machine's `mesh-join-check` passed (no LAN overlap, path holds
1460 bytes), all four ran `wg-mesh`, the 10 core Applications, `core-nodes`
among them, were Synced and Healthy, and the cuts held.

Two things stopped the run on the way. The operator's laptop moved to another
network during `db-healthy`; its SSH session to the Control Plane was dead,
and bootstrap SSH admitted only the old public address. Ctrl-C reached the
close only after the blocked read timed out, about 15 minutes later; the close
read both firewalls back with no port 22, and a rerun opened SSH for the new
address and resumed the database (`resumed: the Cluster is this run's render,
unchanged`). The run then stopped by hand in `monitoring-clean`: the mail load
balancer was refused with `Primary IP limit exceeded`, and `MailPortDown` was
the only alert that fired, with 41 of 41 targets up, as in the rehearsal of
2026-11-19.

## ts1-wk1 joined from behind the site's NAT

`kluster node add ts1-wk1 --provisioner byo --site ts1 --apply` wrote the
machine's entry with the mesh address `10.101.1.1`. The second run took the
VM's LAN address and the host key fingerprint read on its console. The laptop
reached the VM through the Tailscale route of the site's Proxmox host
(`ip route get` of the VM's LAN address gave `dev tailscale0 table 52`), which this run
proved for the first time. In about two minutes it passed every Stage. kluster
logged in with the console key, rotated it, and installed the host. The VM's
`mesh-join-check` found a path of 1460 bytes behind the NAT, and every
machine's `wg-mesh` got the new peer. kluster then logged in again through the
Control Plane at `10.101.1.1`, the first traffic over a NAT peer. After that
came the join and `node-ready` (`Ready, 10.101.1.1, kluster://ts1/ts1-wk1`),
then the profile. Last, the site firewall loaded, after which SSH at the LAN
address no longer answered and kluster dropped that pin.

## The three checks passed

The first check ran from a pod on `ts1-wk1`. It resolved cluster names and an
outside name through CoreDNS on `k8s-cp`. It sent and fetched 4 MiB through a
Service whose two backends stood on `k8swk1` and `k8swk3`. A 5-second upload
reached 73 Mbit/s with retransmits, the site's uplink being the limit. With
the do-not-fragment bit set, a 1322-byte payload passed both ways between
`ts1-wk1` and every other Node, and 1323 bytes were refused, which is the pod
MTU of 1350. Every other Node pulled 4 MiB from the pod on `ts1-wk1`. Flannel's
VXLAN thus reaches a VTEP the kernel routes through `wg-mesh`, one of the two
mesh details the Plan had left unproven.

The second check read the Node, the cloud controller and the load balancer.
The Node had its providerID `kluster://ts1/ts1-wk1`, the taint
`tosak.net/reliability=best-effort:NoSchedule`, the ts1 profile labels and
`node.kubernetes.io/exclude-from-external-load-balancers=true`. The hcloud
cloud controller logged 310 failed lookups of that providerID and not one
delete. The Hetzner API listed the load balancer's targets as the three
Hetzner Workers only. No `hcloud-csi-node` pod ran on `ts1-wk1`, and its
CSINode listed no driver.

For the third check the VM was stopped hard from Proxmox at 23:37:58 UTC. The
Node went `Unknown` at 23:38:41, and at 23:43:54 the test workload that
tolerated the taint ran again on `k8swk3`, moved by the taint-based eviction.
node-controller tainted the Node out of service at 23:59:06 (`not Ready for
20m25s (no-volume)`) and deleted it at 00:04:06, with two DaemonSet pods and no
volume attachment left. The VM, started again at 00:04:22, rejoined `wg-mesh`
by itself, and `kluster node add ts1-wk1` with no flags brought the Node back,
Ready with its profile at 00:05:44.

## The Node does not come back after a reboot by itself

The kubelet's journal showed why the Node was created only at 00:05:31, while
`node add` ran. From 00:04:41 every registration failed with
`lookup k8s-cp.tosak.internal on 127.0.0.53:53: no such host`. Proxmox's
cloud-init data sets `manage_etc_hosts`, so cloud-init wrote `/etc/hosts`
again at boot and dropped the Control Plane endpoint that kluster added at the
join. The `node add` run wrote the line back at 00:05:27, and the kubelet
registered four seconds later without a restart. A Hetzner server's merged
cloud-init configuration has no `manage_etc_hosts`, and its `/etc/hosts` kept
the line. Until kluster keeps the line over a boot, any reboot of the Node VM
loses the Node until `node add` runs. The same pod's `/etc/resolv.conf` also
carried the site router's search domains, the resolver gap that the
[Node VM runbook](../runbook/build-a-node-vm-on-a-self-hosted-site.md) records.
Both wait for a fix before the live join.

## The machine left the cluster clean

`kluster node remove ts1-wk1 --apply` was the first removal of a byo machine.
It drained and deleted the Node, reset kubeadm and the pod network, removed
the site firewall, the mesh configuration and key, and read back that the
machine no longer answered at its mesh address. It then removed the store key,
the Node set entry and the pin, and brought the four Hetzner machines'
`wg-mesh` to their configuration without the peer. The VM took SSH at its LAN
address again, with the key `rotate-host-key` had made, so a next join reads
the fingerprint on the console again. `kluster --env rehearsal down --apply`
then emptied the project and the rehearsal bucket, and a separate read of the
Hetzner API at 00:11:36 UTC counted no server, load balancer, primary IP,
volume, firewall, network or SSH key.
