---
title: "Workers — build runbook"
description: "How kluster adds, removes and replaces a Worker, and every command that built and joined the first three by hand on 2026-09-20, with the checks that proved each step."
type: procedure
topics: [provisioning, kubernetes, networking]
verified: 2026-11-22
---

The numbered steps below were written verbatim as each command ran, on
2026-09-20 ([ADR 0004](../adr/0004-kluster-safety-model.md)), and were the deliverable of Phase 3. kluster now
carries them out: the first sections say how, and each kluster Stage names the
numbered step it ports.

Read [`docs/runbook/rebuild-2026-09-20.md`](./rebuild-2026-09-20.md) for the surrounding plan,
[`docs/runbook/control-plane.md`](./control-plane.md) for the Control Plane this joins to, and
`docs/adr/0006` for why the network settings are what they are.

**Conventions in this file.** A fenced block is a command that was run, exactly
as it was run. Output is quoted only where it was used as evidence. Every
claim about Hetzner state was checked against the API, never read from
Terraform output.

Environment for every Terraform command, from `tools/kluster`:

```
go run . exec -- <command>
```

`kluster exec` reads the environment's Bitwarden item and gives the command the
Hetzner token and the R2 keys; `infra/.envrc` was deleted on 2026-10-20 (see
[the control-plane runbook](./control-plane.md)). The commands below were run
in 2026-09 with those variables exported from that file.

## How kluster carries this out

`kluster up --db fresh|recover` builds every Worker the environment's Worker set
declares, in one run with the Control Plane, the seed and the database
([rehearsal runbook](./rehearsal.md#a-whole-rehearsal-with-kluster-up)). It
creates all the missing Workers with one Terraform apply, builds each with the
code of `node add`, never writes the Worker set, and refuses while a server is
not a Node of the current cluster (delete a dead Worker by hand, or use `node
replace`). The rehearsal's `infra/workers/rehearsal.tfvars` declares `k8swk1`
to `k8swk3` and stays committed. The rest of this section is the single
command.

`kluster node add <name>` builds one Worker, and it runs twice
([ADR 0009](../adr/0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md)). The first run finds the name missing from the environment's
Worker set — `infra/workers/terraform.tfvars` for live,
`infra/workers/rehearsal.tfvars` for a rehearsal — and writes its entry with
the lowest free private and VPN address (the VPN pool skips the hub, the probe
and every peer in `kluster.yaml`). Then it stops. kluster runs no Terraform on a
Worker set git does not hold, because the state in R2 is shared by every clone
and an uncommitted Worker would read as a planned destroy from any other
checkout. The entry's server type is `workers.serverType` from `kluster.yaml`,
or the one `--server-type` names on that first run, so a `cpx32` can join the
`cx23` Workers. Before it writes, kluster asks the Hetzner API whether that type
is offered, in stock and not deprecated in the Control Plane's location, where
the Workers must sit beside the PostgreSQL volumes
([ADR 0005](../adr/0005-storage-and-worker-identity.md)); a type it
cannot create is refused before git holds it. On the second run the flag can
only repeat the declared type. Commit that file alone, then run again:

```sh
cd tools/kluster
./kluster node add k8swk4 --apply          # run 1: the entry, then stop
                                           #   (--server-type cpx32 for another type)
git add ../../infra/workers/terraform.tfvars && git commit -m "…"
./kluster node add k8swk4                  # Plan Mode: the plan and every Stage
./kluster node add k8swk4 --apply          # run 2: create, build, join
```

The second run opens bootstrap SSH (on live the Worker firewall only, in a
rehearsal the Control Plane one too), creates exactly that server with a
seeded host key ([host keys](./host-keys.md)), reads it back over the API, rotates the key at
the first login and runs steps 3 to 8 as Stages, one per step. A Stage whose
Probe passes is skipped, so a run that stopped resumes where it stopped; a
server is resumed only if kluster holds a pin for it. Step 9 runs at the end of
every run, a failed one included, and reads both firewalls back over the API.
On live kluster reaches the Control Plane as the operator through WireGuard
with `sudo`; its pin comes from a one-time `kluster pin import`
([host keys](./host-keys.md#a-server-kluster-did-not-build)).

The commands differ from the by-hand ones below in these places:

| Step | What kluster does differently |
|---|---|
| 2 | Creates one server, not three, with `user_data` (the seeded key); a first plan that creates anything else is refused. Refuses a stale Node of the same name. |
| 3 | First makes sure `enp7s0` holds the private address, as on the Control Plane ([control-plane runbook](./control-plane.md#how-kluster-carries-this-out)). |
| 4 | Never fetches the CNI plugin tarball, as decided below. |
| 6 | Makes the Worker's key on the Worker and reads the hub key with `wg show wg0 public-key`. On the hub it backs up `wg0.conf` to `wg0.conf.bak-kluster-<UTC time>`, appends the peer and adds it with `wg set`; it never restarts the hub, and a resumed run that finds the exact peer writes nothing. |
| 7 | Mints a join token with `--ttl 15m`, writes it to `/run/kluster/join` (tmpfs, 0700) and removes it when the join ends; the token never reaches the terminal. |
| 8 | Waits up to five minutes for the Node: `Ready`, InternalIP the private address, `providerID` `hcloud://<id>`, no `uninitialized` taint. |

`kluster node list` shows the Worker set, the servers and the Nodes side by
side and marks any disagreement. Its PROFILE column is the site label each
Node carries, and a Node whose profile labels differ from its site in the Node
set, or a server outside that site's location, is marked as drift
([Set the node profile](#set-the-node-profile)). In a rehearsal it needs
`kluster ssh open --apply` first and `kluster ssh close --apply` after; with
SSH closed the Node and PROFILE columns show `?`.

Proven on the rehearsal on 2026-10-06: `node add k8swk1` and `k8swk2`, both
runs under `--apply`, ended with both Nodes Ready and "no drift" in `node
list`. The same day `node list --env live` read the three live Workers Ready
with no drift, and a `terraform plan` of `infra/workers` on live showed no
changes.

`versions.kubernetes` in `kluster.yaml` names the exact release the live
Control Plane runs, v1.37.0, and kluster refuses a value without its patch.
It once named the minor alone, and the rehearsal Nodes came up as v1.37.1
beside a v1.37.0 Control Plane. Now the apt channel comes from the minor and
the three packages are installed as `1.37.0-*`; the probe passes only on that
exact patch. A host that already holds another version fails the Stage
instead of being changed, because moving a Node to another release is an
upgrade, not a re-run. The checks were run read-only against the live Control
Plane, and a simulated install there resolved `1.37.0-*` to `1.37.0-1.1`; no
Worker has been built with the pin yet. An upgrade starts by changing that
line.

## Remove a Worker

`kluster node remove <name>` also runs twice, the other way round. The first
run takes the Worker out of the cluster, out of the hub and out of the set, and
leaves the server; the second deletes the server. It refuses a VPN address that
`kluster.yaml` reserves (the hub, the probe, the operator's peer).

```sh
./kluster node remove k8swk4 --apply      # run 1
git add ../../infra/workers/terraform.tfvars && git commit -m "…"
./kluster node remove k8swk4 --apply      # run 2
```

Run 1 first runs the database gate and the last-Worker check described under
"Replace a Worker", then three Stages on the Control Plane, each with a Probe.
A dead Worker that holds a not-ready CloudNativePG instance is refused until
CloudNativePG fails over or the instance is fenced:

1. **drain** — `kubectl drain <name> --ignore-daemonsets --delete-emptydir-data
   --timeout=5m0s`. Done when the Node is gone, or cordoned with only
   DaemonSet, static or finished pods left.
2. **delete-node** — `kubectl delete node <name>`. It refuses a Node that is not
   cordoned.
3. **hub-peer-remove** — finds the hub peer by its `AllowedIPs`,
   `<vpn address>/32`, never by the name in its comment. It refuses a peer that
   routes more than that address, two such peers, or a file and a live `wg
   show` that give the address to different keys. It backs up `wg0.conf`,
   writes it without the peer, then runs `wg set wg0 peer <key> remove`. The
   hub is never restarted.

Then kluster drops its pin at the VPN address and writes the set without the
entry. Run 2 finds the name gone from the set and its server still there. It
refuses while a Node of that name exists, plans exactly one delete of
`hcloud_server.workers["<name>"]`, reads back over the API that the server is
gone, and drops the pin at the public address.

A kubelet registers its Node again when it restarts (assumed, not tested), so
a Worker rebooted between the two runs brings its Node back and run 2 refuses;
drain and delete it again, or run 1 again.

Proven on the rehearsal on 2026-10-06: `k8swk2` and then `k8swk1`, the last
Worker, both runs under `--apply`; the drain of the last Worker passed too.

## Replace a Worker, or all of them

`kluster node replace <name>` makes a Worker new in one run. The set keeps its
entry, so there is no commit between runs. It refuses a name the set does not
declare, and a declared Worker with no server (`node add` builds that).

Before the Worker leaves, the plan and two gates must pass. kluster plans
first and checks the plan against the intent (exactly one replace); a refused
plan leaves the Worker whole, with no command run on the Control Plane and the
pins kept. **The gates:** **The database gate:** every
CloudNativePG Cluster has `status.readyInstances` at least `spec.instances`;
with no CloudNativePG CRD or no Cluster it passes and says so. **The last
Worker:** it refuses to replace the only Ready Worker Node. The plan is
`terraform plan -replace=hcloud_server.workers["<name>"]`, with a new seeded
key in that Worker's `user_data`. Then it runs the three Stages of "Remove a
Worker", drops both pins and applies the saved plan, without a second plan;
Terraform refuses it if the workers state changed since. The new server is built as in
`node add`, steps 3 to 8. If the build stops after the replacement, `kluster
node add <name> --apply` resumes it, because the new server's key is pinned.
A Worker built by hand has no kluster pin, so `node add` refuses its server;
it is built again with `node replace`, never resumed.

```sh
./kluster node replace k8swk1             # Plan Mode
./kluster node replace k8swk1 --apply
./kluster reset --apply                   # every Worker, in name order
```

`kluster reset` replaces every Worker the set declares, one at a time, in name
order. It refuses if any declared Worker has no server. Before each Worker
after the first it waits up to fifteen minutes for the database gate, then
checks both gates again. A stop names the Workers replaced and those not
touched; there is no resume flag, so replace the rest with `node replace`.

Proven on the rehearsal on 2026-10-06, with no CloudNativePG Cluster: `node
replace k8swk1`, then `reset` over two Workers, each new server Ready before
the next began. The first login to each new server verified against the new
seeded key, which proves that `-replace` creates the server with the new
`user_data` although the Module ignores changes to it. The database gate's
CloudNativePG path is not proven yet.

## Set the kubelet reserve

The kubelet keeps back memory for itself and the system so that a burst in the
pods cannot starve them; [ADR 0014](../adr/0014-the-vertical-pod-autoscaler-sizes-the-clusters-own-workloads-and-node-memory-is-their-ceiling.md)
makes that reserve the ceiling of the cluster's own workloads. The values live
in the `kubelet:` block of `kluster.yaml`: `kubeReserved` 160Mi,
`systemReserved` 480Mi, soft eviction at `memory.available` 300Mi after one
minute, and a 30-second grace period for a pod the soft threshold evicts. The
two reserves together cover the memory outside the pods, measured at a p99 of
541 to 651 MiB per Worker from 2026-11-12 to 2026-11-15. Because
`enforceNodeAllocatable` is `[pods]` only, the split between them is
accounting. `evictionHard` is not set and stays at the kubelet default
(`memory.available` 100Mi): a partial `evictionHard` zeroes the defaults of the
other signals.

`kluster node kubelet-config <name>` sets the values on one Worker per run,
in five Stages:

1. **kubelet-room** (Control Plane) — reads the running configuration through
   `configz` and refuses a Worker whose unfinished pods request more memory
   than it offers under the new values, naming the three largest. A restarted
   kubelet admits its pods again and rejects with `OutOfmemory` those that no
   longer fit. Done when the kubelet already runs the values.
2. **kubelet-configmap** (Control Plane) — merges the values into
   `kube-system/kubelet-config` (`data.kubelet`), with a unified diff in Plan
   Mode. kubeadm writes this ConfigMap into every node it joins, so a new
   Worker gets the values at its join (assumed from kubeadm's design, not yet
   proven on a build).
3. **kubelet-config-file** (Worker) — backs up `/var/lib/kubelet/config.yaml`
   to `config.yaml.bak-kluster-<UTC time>` and runs `kubeadm upgrade node phase
   kubelet-config`, which writes the file from the ConfigMap and keeps the
   `containerRuntimeEndpoint` from `instance-config.yaml`.
4. **kubelet-restart** (Worker) — refuses while the file does not hold the
   values, runs `systemctl restart kubelet` and waits up to two minutes for
   `configz` to show them.
5. **kubelet-proof** (Control Plane) — waits up to two minutes for the Node to
   be Ready, `configz` to hold the values, allocatable memory to equal capacity
   minus both reserves and `evictionHard`, and no pod on the Node to be Failed
   with a reason that starts with `OutOf` or with `UnexpectedAdmissionError`.

A Stage whose Probe passes is skipped, so a stopped run resumes. On live
kluster reaches both hosts through WireGuard as the operator with `sudo` and
opens no firewall. It dials both before any Stage runs, so a Worker without a
pin at its VPN address stops the run before anything changes; the pin comes
from a one-time `kluster pin import <vpn address>`
([host keys](./host-keys.md#a-server-kluster-did-not-build)). A rehearsal
opens bootstrap SSH on both firewalls and closes it at the end; there Plan
Mode lists the Stages without reading either host.

```sh
cd tools/kluster
kluster pin import 10.100.0.3             # once per Worker
kluster node kubelet-config k8swk2        # Plan Mode: room, ConfigMap diff
kluster node kubelet-config k8swk2 --apply
```

Run one Worker at a time and check the room of the next before it starts: the
reserve takes about 640 MiB from each Worker's allocatable memory. On live on
2026-11-16 a Worker's capacity read 3911564Ki and its allocatable 3809164Ki
(`kubectl get node -o json`); under the new values it is 3153804Ki, about
3079 MiB.

Run on live on 2026-11-16 in the order k8swk2, k8swk1, k8swk3. Every
Worker then read 3153804Ki to 3153812Ki allocatable, `configz` held the
values with `containerRuntimeEndpoint` kept, and no pod was Failed. The pods
on k8swk2 requested 1760 MiB and those on k8swk1 2768 MiB; on k8swk3 they
requested 3072 MiB, a margin of 7 MiB, beside the PostgreSQL primary. Room
was made first: `kubectl cordon k8swk3`, then the Grafana pod (784 MiB, no
volume) was deleted and came back on k8swk2, and the run found 2288 MiB;
`kubectl uncordon k8swk3` followed.

🔴 **A cordon on the node of the CloudNativePG primary starts a switchover.**
CloudNativePG moved the primary from `tosak-pg-cluster-1` to
`tosak-pg-cluster-2` within eight seconds of the cordon ("Current primary is
running on unschedulable node k8swk3"), a short write outage, and restarted
the old primary as a replica; the Cluster was healthy again with 3 of 3
instances ready. Before a cordon, read `status.currentPrimary` of the Cluster
and the node its pod runs on.

## Set the node profile

Every Node carries the Capability Profile of its site as admin labels under
`tosak.net.node-restriction.kubernetes.io/`: `provider`, `site`,
`reliability`, `reach`, and `block-volumes` and `load-balancer` when the site
has them ([ADR 0016](../adr/0016-every-provider-is-a-set-of-capabilities-and-workers-of-any-provider-join-as-equals.md)).
The profiles live in the environment's Node set file,
[`infra/nodes/live.yaml`](../../infra/nodes/live.yaml) or
[`infra/nodes/rehearsal.yaml`](../../infra/nodes/rehearsal.yaml). The Control
Plane and each Worker of the Worker set belong to the site whose provider is
`hcloud`, `hel1`, so all of them carry the same labels today. No workload is
placed by them yet.

`kluster node profile [<name>]` writes and checks the labels on every Node, or
on the named one, in one **node-profile** Stage per Node run on the Control
Plane. The Stage compares the Node's labels under the prefix with the site's
profile. It is done when they match; otherwise it sets the missing or wrong
values and removes any other key under the prefix with one `kubectl label node
… --overwrite`, and reads them back. Labels outside the prefix are never
touched. `node add`, `node replace`, `cp init` and `up` run the same Stage
after the Node is Ready, so a new Node joins with its profile.

kluster refuses, and does not label, a Node that is neither the Control Plane
nor in the Worker set (it knows no site for it), a Node with no Hetzner server,
and a Node whose server stands in a location other than the site's name. The
other Nodes are still labelled, and the run fails at the end. The same location
check runs before `node add` creates a server and in the preflight of `up`, so
a Node set that does not describe the project stops a build before any money
is spent.

```sh
cd tools/kluster
kluster node profile                 # Plan Mode: each Node's change, or done
kluster node profile --apply
kluster node list                    # PROFILE hel1, no drift
```

On live kluster reaches the Control Plane through WireGuard and opens nothing.
A rehearsal opens bootstrap SSH on the Control Plane firewall under `--apply`
and closes it at the end; there Plan Mode reads no Node.

Run on live on 2026-11-17. The run labelled `k8s-cp`, `k8swk1`, `k8swk2` and
`k8swk3` with `provider=hcloud`, `site=hel1`, `reliability=high`,
`reach=public`, `block-volumes=hcloud` and `load-balancer=hcloud`, and
`kubectl get nodes -L` with the six keys read them back on all four. `node list`
then showed PROFILE `hel1` and no drift, and a second Plan Mode run found every
Stage done.

## Join the mesh

`wg-mesh` joins machines at different sites
([ADR 0016](../adr/0016-every-provider-is-a-set-of-capabilities-and-workers-of-any-provider-join-as-equals.md)).
A Node set without a `mesh` section changes nothing, and neither live nor the
rehearsal has one yet. 🔴 `kluster node mesh` has not run on a cluster. The
renderer's unit tests and a WireGuard test in network namespaces are its proof
so far: a direct pair, a NAT pair and a relay between two NAT sites, with
full-size packets on a 1460-byte link.

```sh
cd tools/kluster
go test -tags wireguard -run TestWireGuard -v ./internal/mesh/
```

The Node set file declares each site's subnet and each machine's address:

```yaml
sites:
  hel1:
    # … the site's profile
    meshSubnet: 10.101.0.0/24
  ts1:
    # … the site's profile
    meshSubnet: 10.101.1.0/24
mesh:
  relay: k8swk1 # required with machines on two or more NAT sites
  machines:
    k8swk1: { site: hel1, address: 10.101.0.11 }
    k8swk2: { site: hel1, address: 10.101.0.12 }
```

A `meshSubnet` is a `/24` inside `10.101.0.0/16`, and no two sites share one. An
address lies in its site's subnet and is unique. The relay is a machine of the
mesh on a public site. A site whose provider is not `hcloud` holds one machine,
because kluster does not build the LAN routes between two machines of such a
site yet. kluster logs in only to the Control Plane and the Worker set, so it
refuses a machine outside Hetzner until the bring-your-own step exists.

`kluster node mesh [<name>]` joins every machine of the section, or the named
one. `node add`, `node replace`, `cp init` and `up` run the same steps for a
machine the section declares.

```sh
kluster node mesh           # Plan Mode: each machine's Stages
kluster node mesh --apply
```

| Stage | What it does | When it fails |
|---|---|---|
| **mesh-join-check** | Compares the machine's LAN with the cluster's ranges and measures the link MTU | A LAN overlaps `10.0.0.0/16`, `10.244.0.0/16`, `10.96.0.0/16`, `10.100.0.0/24` or `10.101.0.0/16`, or the link or its path holds less than 1460 bytes |
| **mesh-key** | Installs WireGuard and makes `/etc/wireguard/mesh.key`; only the public key leaves the machine | The package or the key cannot be written |
| **wireguard-mesh** | Writes `/etc/wireguard/wg-mesh.conf`, applies it with `wg syncconf`, reconciles the routes and enables `wg-quick@wg-mesh` | The interface, its peers or its routes differ from the rendered config |

A failed join check or key stops the run before any config changes. After them
kluster writes each new or changed public key to the ConfigMap
`kube-system/mesh-peers`. Every joined machine then gets its config, even on a
named run, because a new key changes the peers of the others. A machine that
fails its config does not stop the rest, and the run fails at the end.

kluster compares the store with the Node set before it changes anything. After a
machine leaves the `mesh` section, the run is refused until its data key is
removed from `kube-system/mesh-peers`; `node remove` does not remove it.

The store keeps no history. Its public record is the mesh catalog: `peers.json`
on the branch `mesh-catalog`, with each machine's name, site, mesh address and
public key, and never an endpoint. A CronJob in the namespace `nodes` compares
the store with the catalog every hour and sends the store to a workflow when the
two differ, so a hand edit is recorded too, but a change put back within the
hour leaves no trace. The [nodes README](../../core/nodes/README.md) shows the
flow and how to start a run by hand.

UDP 51821 stays closed on both Hetzner firewalls until `allow_mesh_udp` is set in
a committed `infra/shared/terraform.tfvars`. The
[infra README](../../infra/README.md#the-mesh-port) says why a `-var` does not
hold, and how to prove the rule is gone after the switch is turned off.

## Change the pod MTU

A pod packet between sites is VXLAN inside the WireGuard mesh, so
[ADR 0016](../adr/0016-every-provider-is-a-set-of-capabilities-and-workers-of-any-provider-join-as-equals.md)
plans packet sizes for that path: Flannel's `Backend.MTU` in
`core/cni/flannel.yaml` is 1400, and pods get 1350, which is `podMTU` in
`kluster.yaml`. `Backend.MTU` is the underlay value and Flannel subtracts the
50-byte VXLAN header itself
([control-plane runbook](./control-plane.md#correction--the-mtu-was-set-twice));
a test in `tools/kluster` refuses a manifest whose value is not `podMTU` + 50.
A build applies the manifest, and the `cni-flannel` Stage checks `flannel.1`
against `podMTU`. Argo CD does not sync `core/cni`, so a merge changes only the
next build.

A running cluster needs more than an apply. Flannel does not resize an
existing `flannel.1`, and Flannel v0.28.9 watches the device: a deleted
`flannel.1` is made again at once by the running `flanneld`, at the MTU of the
configuration that `flanneld` started with, and without its routes, FDB entries
or neighbours, which only a `flanneld` start programs. A pod keeps the MTU its
interface got at creation until it is replaced. `kluster node pod-mtu` makes
the change in four Stages:

1. **flannel-config** (Control Plane) — patches only `net-conf.json` of
   `kube-flannel/kube-flannel-cfg` from `core/cni/flannel.yaml`, with a diff
   in Plan Mode. A ConfigMap change starts no rollout.
2. **flannel-mtu** (every Node, the Control Plane included, one at a time) —
   deletes the Node's Flannel pod and waits until the new pod has written
   `FLANNEL_MTU`, deletes `flannel.1` and waits until that pod has made it
   again at the pod MTU, deletes the Flannel pod once more so that the next
   start programs the peers, waits for one `flannel.1` route per other Node,
   then sets `cni0`. Done when `flannel.1`, `/run/flannel/subnet.env`, the
   routes and `cni0` hold the value.
3. **pod-mtu-restart** (Control Plane) — reads the `eth0` MTU of every pod
   network namespace on every Node, joins them to pods by IP, and restarts the
   owner of each pod at another MTU, one at a time: `rollout restart` and
   `rollout status --timeout=5m` for a Deployment, StatefulSet or DaemonSet,
   then each CloudNativePG Cluster last (its replicas deleted, a switchover
   through `status.targetPrimary`, the old primary deleted). Bare pods, Jobs
   and ReplicaSets without a Deployment are reported, not restarted. The first
   restart that does not finish stops the run.
4. **pod-mtu-proof** (Control Plane) — waits up to three minutes for every
   Node Ready, no pod Pending, every database healthy and every workload's
   pods at the pod MTU.

A Stage whose Probe passes is skipped, so a stopped run resumes. On live
kluster reaches every Node through WireGuard and dials all of them before any
Stage runs; a rehearsal opens bootstrap SSH and closes it at the end.
`--until flannel` stops after Stage 2, which leaves old pods sending through
tunnels of the new size; path MTU discovery covers that state (proven below).

```sh
cd tools/kluster
kluster node pod-mtu                          # Plan Mode: the diff, every restart
kluster node pod-mtu --until flannel --apply  # tunnels only
kluster node pod-mtu --apply                  # restarts and the proof
```

Each Deployment with the `Recreate` strategy is down during its restart, and
the switchover stops database writes for a few seconds. On live on 2026-11-22
nine Deployments used `Recreate`: `argocd-notifications-controller`,
`authos-api`, `authos-demo`, `authos-ui`, `duster`, `barman-cloud`, `doma`,
`stalwart` and `redis`.

### The proof

A throwaway DaemonSet in a `netcheck` namespace runs one
`nicolaka/netshoot:v0.16` pod per Node, each an `iperf3` server. For every
ordered pair of pods on two Nodes (12 pairs), a ping with the don't-fragment
bit (`ping -M do -s <payload>`) at the largest payload that fits must pass, the
next larger must fail, and a 4 MiB `iperf3` pull (`-R`) must complete. A pull
is the check that matters: an MTU fault shows as a TCP black hole, where small
packets pass and large ones vanish. The namespace is deleted afterwards.

| State | Passes | Fails | 4 MiB pulls |
|---|---|---|---|
| Before: tunnels and pods at 1400 | 1372 | 1373, refused by the sender (`Message too large`) | 12 of 12 |
| After Stage 2: tunnels 1350, pods 1400 | 1322 | 1372, `Frag needed and DF set (mtu = 1350)` from the path | 12 of 12 |
| After Stage 4: pods 1350 | 1322 | 1323, refused by the sender | 12 of 12 |

The same three states passed in a rehearsal on 2026-11-19 and on live on
2026-11-22. On live, Stages 1 and 2 took 72 seconds for four Nodes. The full
run took eight minutes (08:49:52 to 08:57:42 UTC): 36 rollouts, then the
CloudNativePG switchover from `tosak-pg-cluster-2` to `tosak-pg-cluster-1`, and
the proof. While the `barman-cloud` plugin restarted, the Cluster reported
"cannot proceed to reconciliation due to an error while interacting with
plugins" for about ten seconds and was healthy again before its own turn.
Afterwards every Argo CD Application was Synced and Healthy, and none of the
13 public-host and mail-port probes had recorded a failure in the last 15
minutes (5 samples each, so an outage of a few seconds is not excluded). The
only firing alert was Watchdog, but that check came too early: the run had
restarted Prometheus at 08:54, which deletes its history, and every alert
waited out its `for:` again. Fifteen `ContainerMemoryNearRequest` alerts
(`for: 30m`) were pending from 08:55 and fired from about 09:25. Whether they
fired before the run is not known.

🔴 **The rehearsal found that deleting `flannel.1` under a running `flanneld`
does not change its MTU.** The first version of Stage 2 deleted the device
first; the old `flanneld` made it again at 1400 and the new pod kept it. That is
why Stage 2 now restarts Flannel before and after the delete.

## Why this was done by hand

`infra/scripts/bootstrap/bootstrap_workers.sh` and the `kluster` CLI both call
the same four-stage pipeline, and neither is usable as written. Five defects
were known or found before a command was run:

1. **`kubeadm join` would fail on every Worker.** `kubeadm token create
   --print-join-command` prints the control-plane endpoint
   `k8s-cp.tosak.internal:6443` ([ADR 0006](../adr/0006-network-paths-and-control-plane-endpoint.md)). No Worker resolves that name, and
   `bootstrap_node-4-kubernetes.sh` writes no `/etc/hosts` line.
2. **The driver would destroy the hub config.** It rebuilds the Control
   Plane's `/etc/wireguard/wg0.conf` from a template, discarding the hub
   config written by hand in step 3 of the Control Plane runbook. Peers must
   be appended, never regenerated.
3. The driver reads `$HOME/k8s/infra/stage/out/worker_ips.txt`, which does not
   exist, and still assigns VPN addresses by counting lines in it — the defect
   ADR 0006 removed by declaring `vpn_ip` per name in `terraform.tfvars`.
4. **The CNI plugin pin has never worked.** Stage 3 extracts the 1.9.0 tarball
   into `/opt/cni/bin`; stage 4's `kubelet` pulls in the `kubernetes-cni`
   package, which overwrites it and becomes the dpkg owner. `kubernetes-cni`
   is not in the `apt-mark hold` set either.
5. **`adduser --disabled-password` plus the `sudo` group gives an unusable
   sudo.** No password exists to answer the prompt. Fixed on the Control Plane
   with a `NOPASSWD` sudoers drop-in.

So the procedure below is the record, and the scripts are corrected from it
afterwards. That is the order [ADR 0004](../adr/0004-kluster-safety-model.md) asks for.

---

## 0. Starting state

| | |
|---|---|
| Servers | one — `k8s-cp` `166646798`, `Ready`, v1.37.0 |
| `tosak-cp-firewall` `10289761` | `in udp 51820 0.0.0.0/0`, applied to `166646798` |
| `tosak-worker-firewall` `11651947` | no rules, applied to no server |
| Pending pods | `hcloud-csi-controller` only — it waits for a Worker, by design |
| Workstation public IP | `185.100.244.43` |

The worker set, declared in `infra/workers/terraform.tfvars`:

| Name | private | VPN | type |
|---|---|---|---|
| k8swk1 | 10.0.2.6 | 10.100.0.2 | cx23 |
| k8swk2 | 10.0.2.7 | 10.100.0.3 | cx23 |
| k8swk3 | 10.0.2.8 | 10.100.0.4 | cx23 |

---

## 1. Open bootstrap SSH on the worker firewall

A new Worker has no tunnel until its WireGuard stage runs, and that stage runs
over SSH. So the first login must use the public address, and port 22 must be
open to the workstation while the build runs. It is opened **once** for the
whole build and closed once at the end (step 9) — every close is a
one-rule-to-none update, which is the hcloud 1.69.0 defect, so three closes
would mean three manual cleanups instead of one check.

```
terraform -chdir=shared plan -var="allow_public_ssh_worker=true" -out=worker-ssh-open.tfplan
terraform -chdir=shared apply worker-ssh-open.tfplan
```

```
Plan: 0 to add, 1 to change, 0 to destroy.
```

Zero create, zero destroy, zero replace is the gate. This is a zero-rule-to-one
update, which the provider performs correctly; the defect is the reverse
direction.

```
Apply complete! Resources: 0 added, 1 changed, 0 destroyed.
```

### Verified over the Hetzner API

```
curl -s -H "Authorization: Bearer $TF_VAR_HCLOUD_TOKEN" \
  https://api.hetzner.cloud/v1/firewalls | jq .
```

| Firewall | Rules | Applied to |
|---|---|---|
| `10289761` `tosak-cp-firewall` | `in udp 51820 0.0.0.0/0` | `166646798` |
| `11651947` `tosak-worker-firewall` | `in tcp 22 185.100.244.43/32` | none |

`allow_public_ssh_cp` was **not** touched. The Control Plane stays closed on
port 22 for the whole of Phase 3; it is reached over the VPN.

No server is attached to the worker firewall yet, so nothing became reachable.

---

## 2. Create the three servers

```
terraform -chdir=workers init
terraform -chdir=workers plan -out=workers.tfplan
terraform -chdir=workers apply workers.tfplan
```

```
Plan: 3 to add, 0 to change, 0 to destroy.
```

Zero destroy and zero replace is the gate. Read the plan for what it does
**not** contain: the names are literally `k8swk1`, `k8swk2`, `k8swk3`, with no
suffix of any kind. `var.node_suffix` is gone and nothing replaced it, so the
mechanism that renamed and therefore destroyed every worker on 2026-09-13
cannot be expressed by this module ([ADR 0005](../adr/0005-storage-and-worker-identity.md)).

```
Apply complete! Resources: 3 added, 0 changed, 0 destroyed.

worker_public_ips = {
  "k8swk1" = "135.181.154.56"
  "k8swk2" = "62.238.56.128"
  "k8swk3" = "2.29.31.80"
}
```

### Verified over the Hetzner API

```
curl -s -H "Authorization: Bearer $TF_VAR_HCLOUD_TOKEN" \
  https://api.hetzner.cloud/v1/servers | jq .
```

| Name | id | type | location | public IPv4 | IPv6 | private | firewall | protection | labels |
|---|---|---|---|---|---|---|---|---|---|
| k8swk1 | `166652126` | cx23 | hel1 | 135.181.154.56 | none | 10.0.2.6 | `11651947` applied | none | `role=worker` |
| k8swk2 | `166652125` | cx23 | hel1 | 62.238.56.128 | none | 10.0.2.7 | `11651947` applied | none | `role=worker` |
| k8swk3 | `166652124` | cx23 | hel1 | 2.29.31.80 | none | 10.0.2.8 | `11651947` applied | none | `role=worker` |

The project now holds exactly four servers. All four are in `hel1`, which
[ADR 0005](../adr/0005-storage-and-worker-identity.md) requires: Hetzner Volumes are location-bound, so a Worker must sit
where the PostgreSQL volumes are.

Workers carry **no** delete or rebuild protection, unlike the Control Plane.
That is deliberate ([ADR 0002](../adr/0002-control-plane-isolation.md)): a Worker is meant to be replaceable.

**API shape note.** `/v1/servers` now returns `location` at the top level of
each server and no `datacenter` object at all. A `.datacenter.location.name`
query returns `null` rather than failing. This is the second such change found
during the rebuild — `/v1/actions` was removed earlier, in favour of
`/v1/servers/actions`. Read the keys before trusting a path.

### Stale host keys, before the first login

```
for ip in 135.181.154.56 62.238.56.128 2.29.31.80 \
          10.100.0.2 10.100.0.3 10.100.0.4; do
  ssh-keygen -F "$ip" >/dev/null && echo "$ip STALE" || echo "$ip clean"
done
```

The three public addresses are newly allocated and clean. The three **VPN**
addresses are not: `10.100.0.2`, `10.100.0.3` and `10.100.0.4` still hold the
dead Workers' host keys, because VPN addresses are assigned by this repo and
are therefore always reused.

This is the same lesson the Control Plane taught at `46.62.209.249` and again
at `10.100.0.1`: **a reused address means a stale `known_hosts` entry at every
address the host answers on.** The public entry being clean proves nothing
about the VPN entry. Each is cleared at the point the address is first used,
so the removal is recorded in the step that needs it.

---

## 3. Base host setup

Run as `root` over the **public** address on each Worker. The Hetzner image
injects the cluster SSH key into `root` only, so the first login of any
rebuilt host is `root`; `tosak` does not exist until this step creates it.

```
ssh -i ~/.ssh/hetzner-cluster root@135.181.154.56   # k8swk1
ssh -i ~/.ssh/hetzner-cluster root@62.238.56.128    # k8swk2
ssh -i ~/.ssh/hetzner-cluster root@2.29.31.80       # k8swk3
```

All three public host keys were new and accepted on first contact. Hetzner
publishes no host key over the API, so this is trust on first use, the same
as the Control Plane.

### 🔴 The private network attach races cloud-init

The starting readback found the private interface configured on **one** of the
three:

```
k8swk1  lo eth0                 enp7s0 DOWN, no address
k8swk2  lo eth0 enp7s0=10.0.2.7 enp7s0 UP
k8swk3  lo eth0                 enp7s0 DOWN, no address
```

`/etc/netplan/50-cloud-init.yaml` on `k8swk1` declared only `eth0`. On
`k8swk2` it declared both. The Hetzner API reported the network attached on
all three, so this is entirely guest-side: `hcloud_server` creates the server
and *then* attaches the network, and whether that lands before cloud-init's
network stage is a race. Which hosts win it is luck.

**This would have broken the old pipeline intermittently.** Stage 4 of
`infra/scripts/bootstrap/pipeline/` reads `ip -4 addr show enp7s0` and exits
when it is empty. A build would have failed on some hosts and not others, with
no indication that the cause was timing.

The fix is a netplan drop-in, written unconditionally — a no-op where
cloud-init already did the job:

```
/etc/netplan/60-private-net.yaml   (mode 600)

network:
  version: 2
  ethernets:
    enp7s0:
      dhcp4: true
      optional: true
```

then `netplan apply` and wait for the address. `optional: true` keeps a boot
from blocking on the interface if it is ever genuinely absent.

### The rest of the step

Run through `ssh root@<public> 'bash -s' < worker-base.sh`, idempotent:

1. The netplan drop-in above.
2. User `tosak` — `adduser --disabled-password`, `usermod -aG sudo`,
   `root`'s `authorized_keys` copied to `/home/tosak/.ssh/`, and
   **`/etc/sudoers.d/tosak` carrying `tosak ALL=(ALL) NOPASSWD:ALL`**,
   validated with `visudo -cf`. Without that drop-in the account has no
   password, so a sudo prompt can never be answered and membership of the
   `sudo` group alone is unusable. This is the same defect and the same fix as
   `cp-dev` on the Control Plane.
3. `/etc/modules-load.d/k8s.conf` = `overlay`, `br_netfilter`, both
   `modprobe`d; `/etc/sysctl.d/k8s.conf` = the three bridge and forward
   values, then `sysctl --system`.
4. `swapoff -a` and swap commented in `/etc/fstab`. No swap was present; this
   is a guard.
5. `chrony` installed and enabled.

### Readback, all three identical

| Check | k8swk1 | k8swk2 | k8swk3 |
|---|---|---|---|
| `enp7s0` | 10.0.2.6 | 10.0.2.7 | 10.0.2.8 |
| `enp7s0` MTU | 1450 | 1450 | 1450 |
| `tosak` | uid 1000, groups `tosak sudo users` | same | same |
| `sudo -n true` as `tosak` | ok | ok | ok |
| `overlay` + `br_netfilter` | 2 of 2 | 2 of 2 | 2 of 2 |
| three sysctls | `1 1 1` | `1 1 1` | `1 1 1` |
| swap | none | none | none |
| chrony | active, `Leap status: Normal` | same | same |
| `ping 10.0.1.5` | reachable | reachable | reachable |
| `10.0.1.5:6443` | open | open | open |

**MTU 1450 is independent confirmation of the Flannel fix.** `core/cni/flannel.yaml`
carries `"MTU": 1450` as the *underlay* value and flannel subtracts the 50-byte
VXLAN overhead itself, giving pods 1400. 1450 is what the private interface
actually reports.

The API server is already reachable over the private network from every
Worker. The VPN is **not** needed to join a Worker — it exists so the operator
can reach the Worker ([ADR 0006](../adr/0006-network-paths-and-control-plane-endpoint.md)).

### A correction to the Phase 2 notes

The Control Plane runbook records chrony "synced to time.cloudflare.com". The
Workers synced to Canonical hosts instead, which looked like a deviation. It
is not: `/etc/chrony/chrony.conf` on the Control Plane carries the stock Ubuntu
pool lines and no explicit server. All four hosts run the same configuration,
and `time.cloudflare.com` was simply what `ntp.ubuntu.com` resolved to that
day. **No time source is pinned anywhere in this cluster.**

### A defect in the local SSH config, found here

`ssh cp-authos` now hangs. Its `HostName` is `46.62.209.249`, the public
address, and step 9 of the Control Plane runbook closed port 22 there. The
entry was correct when it was written and was made wrong by the close. The
only path to the Control Plane is now `ssh cp-dev@10.100.0.1`, over the VPN.

---

## 4. Container runtime

Run as `root` on each Worker.

```
wget -qO- https://github.com/containerd/containerd/releases/download/v2.2.0/containerd-2.2.0-linux-amd64.tar.gz | tar -C /usr/local -xz
mkdir -p /usr/local/lib/systemd/system
curl -fsSL https://raw.githubusercontent.com/containerd/containerd/v2.2.0/containerd.service -o /usr/local/lib/systemd/system/containerd.service
systemctl daemon-reload
systemctl enable --now containerd

mkdir -p /etc/containerd
containerd config default > /etc/containerd/config.toml
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
systemctl restart containerd

wget -q https://github.com/opencontainers/runc/releases/download/v1.4.0/runc.amd64 -O /tmp/runc.amd64
install -m 755 /tmp/runc.amd64 /usr/local/sbin/runc
```

The systemd unit is fetched from the **release tag**, `v2.2.0`, not from
`main`. `bootstrap_node-3-containerd.sh` uses `main`, which can change under a
pinned binary. The two were byte-identical on the day, so pinning cost
nothing. Same deviation as the Control Plane.

### 🔴 The CNI plugin tarball is dropped, deliberately

`bootstrap_node-3-containerd.sh` extracts `cni-plugins-linux-amd64-v1.9.0.tgz`
into `/opt/cni/bin` here. It is **not** run, because it has never had any
effect: the `kubernetes-cni` package that `kubelet` depends on installs into
the same directory one step later and becomes the dpkg owner of every file in
it. The Control Plane runs `kubernetes-cni` 1.9.1 for exactly this reason. The
download was dead weight, and removing it makes the Workers match the Control
Plane rather than pretend to a pin that never held.

### Readback, all three identical

| Check | Value |
|---|---|
| containerd | `v2.2.0` `1c4457e00facac03ce1d75f7b6777a7a851e5c41` |
| runc | `1.4.0` |
| service | active, enabled |
| `SystemdCgroup = true` | present |
| `/opt/cni/bin` | absent — correct at this point |

---

## 5. Kubernetes packages and pre-join configuration

Run as `root` on each Worker. The apt channel is **v1.37**, matching the
Control Plane. `infra/scripts/utils/install_kubeadm.sh` and stage 4 of the
pipeline were both moved from v1.34 to v1.37 in an earlier session; without
that the Workers would land three minors behind.

```
curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.37/deb/Release.key | gpg --dearmor --yes -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.37/deb/ /' > /etc/apt/sources.list.d/kubernetes.list
apt-get update && apt-get install -y kubelet kubeadm kubectl
apt-mark hold kubelet kubeadm kubectl kubernetes-cni
```

### 🔴 `kubernetes-cni` is in the hold set

It was not in the original. It is the dpkg owner of every file in
`/opt/cni/bin`, so leaving it unheld lets an `apt upgrade` move the plugin set
underneath a pinned kubelet, silently. Four packages are held, not three.

### 🔴 The control-plane endpoint must be in `/etc/hosts` before the join

```
echo '10.0.1.5  k8s-cp.tosak.internal' >> /etc/hosts
```

`kubeadm token create --print-join-command` prints
`kubeadm join k8s-cp.tosak.internal:6443 …`, because `kubeadm init` was given
`--control-plane-endpoint` ([ADR 0006](../adr/0006-network-paths-and-control-plane-endpoint.md)). `.internal` is ICANN-reserved and this
name is deliberately not in public DNS, so every node resolves it from its own
`/etc/hosts`. **Nothing in the pipeline writes this line.** Without it the
join cannot find the API server, and the failure looks like a DNS problem
rather than a missing step.

### kubelet arguments

```
KUBELET_EXTRA_ARGS=--node-ip=<private ip> --cloud-provider=external
```

`--node-ip` pins the InternalIP to the private network instead of letting
kubelet pick the public NIC. `--cloud-provider=external` leaves the node
carrying `node.cloudprovider.kubernetes.io/uninitialized` until the hcloud CCM
writes its `providerID`; without it the CSI driver cannot attach volumes. Both
are defaults that point the wrong way if left alone.

`systemctl enable kubelet` only. It stays **inactive** until the join, because
it has no configuration yet. That is correct, not a fault.

### Readback

| Check | k8swk1 | k8swk2 | k8swk3 |
|---|---|---|---|
| kubeadm / kubelet / kubectl | v1.37.0 | v1.37.0 | v1.37.0 |
| holds | `kubeadm kubectl kubelet kubernetes-cni` | same | same |
| `/opt/cni/bin` owner | `kubernetes-cni` | same | same |
| CNI plugins | 20 | 20 | 20 |
| `getent hosts k8s-cp.tosak.internal` | `10.0.1.5` | `10.0.1.5` | `10.0.1.5` |
| `--node-ip` | 10.0.2.6 | 10.0.2.7 | 10.0.2.8 |
| kubelet | enabled / inactive | same | same |

---

## 6. WireGuard

A Worker does **not** need the VPN to join the cluster — it reaches the API
server over the private network, which step 3 already proved. The tunnel
exists so the operator can reach the Worker, and so the Worker's port 22 need
never be open publicly ([ADR 0006](../adr/0006-network-paths-and-control-plane-endpoint.md)).

### Worker side, each host as `root`

```
apt-get install -y wireguard
wg genkey > /etc/wireguard/private.key
wg pubkey < /etc/wireguard/private.key > /etc/wireguard/public.key
```

```
/etc/wireguard/wg0.conf   (mode 600)

[Interface]
Address    = <vpn ip>/24
PrivateKey = <generated>

[Peer]
# k8s-cp, the VPN hub and router
PublicKey           = Cy2AjXYE8BHjoPJkDocHywi9ZYMADOljW5qvKUIUngk=
AllowedIPs          = 10.100.0.0/24
Endpoint            = 46.62.209.249:51820
PersistentKeepalive = 25
```

```
systemctl enable wg-quick@wg0
systemctl restart wg-quick@wg0
```

Two details carry the hub-and-spoke design:

- **No `ListenPort`.** The spoke dials out from an ephemeral port, so it never
  needs an inbound WireGuard rule. This is exactly why `tosak-worker-firewall`
  holds nothing in its steady state, and why one shared firewall would have
  been wrong.
- **`AllowedIPs = 10.100.0.0/24`, not just the hub's address.** The Control
  Plane is the hub *and* the router between peers, so the operator's laptop at
  `10.100.0.69` is reached through it. `net.ipv4.ip_forward` is already `1` on
  the Control Plane.

Generated public keys:

| Worker | VPN | public key |
|---|---|---|
| k8swk1 | 10.100.0.2 | `ZPJXxqmB+FJiSATDOEF6KXIf1aAoe+2p0B/0CqLkSU4=` |
| k8swk2 | 10.100.0.3 | `UC0IQXJAVDXOA9lsKoqwtcN0qRtYC94H0Tmf079C0Wk=` |
| k8swk3 | 10.100.0.4 | `is9OmqLOARa4HFU8Thx1FuUEIo4Vt63YTVEc2TlhD10=` |

### 🔴 Hub side: append, never regenerate

```
cp /etc/wireguard/wg0.conf /etc/wireguard/wg0.conf.bak-phase3-2026-09-20
# for each worker: append a [Peer] block to the file, then
wg set wg0 peer <public key> allowed-ips <vpn ip>/32
```

Two rules, both learned the hard way:

- **The hub's `wg0.conf` is appended to, never rewritten.**
  `bootstrap_workers.sh` rebuilds it from a template — it would have discarded
  the hub config written by hand in step 3 of the Control Plane runbook,
  including the operator's own peer.
- **`wg-quick@wg0` is not restarted on the hub.** The operator's SSH session
  to the Control Plane runs over that tunnel, so a restart cuts the session
  that is performing the change. `wg set` is live and additive; the file edit
  only has to survive a reboot.

The placeholder comment left in step 3 of the Control Plane runbook was
removed once the real peers replaced it.

### Verification

Handshakes did not all land at once. `k8swk3` connected immediately;
`k8swk1` and `k8swk2` had already started their interfaces before the hub knew
their keys, so their first handshakes were rejected and `PersistentKeepalive`
retried them in. All three were up within one keepalive interval. **A Worker
starting its tunnel before it is peered is normal and self-correcting**; it is
not a reason to restart anything.

From the Control Plane, all three VPN addresses answer. From the laptop, all
three answer too — which proves the hub is routing between peers, not just
terminating them.

### Stale host keys at the VPN addresses

```
cp ~/.ssh/known_hosts ~/.ssh/known_hosts.bak-phase3-2026-09-20
ssh-keygen -R 10.100.0.2
ssh-keygen -R 10.100.0.3
ssh-keygen -R 10.100.0.4
```

All three held the **dead** Workers' host keys, exactly as predicted in step 2.
The public addresses were clean because they were newly allocated; the VPN
addresses are assigned by this repo and are therefore always reused. Without
this, the first VPN login would have failed with REMOTE HOST IDENTIFICATION
HAS CHANGED.

### The operator route, proven before port 22 is withdrawn

```
ssh -i ~/.ssh/hetzner-cluster tosak@10.100.0.2
```

| Worker | hostname | `$SSH_CONNECTION` client | `sudo -n` |
|---|---|---|---|
| k8swk1 | k8swk1 | 10.100.0.69 | ok |
| k8swk2 | k8swk2 | 10.100.0.69 | ok |
| k8swk3 | k8swk3 | 10.100.0.69 | ok |

The client address is the laptop's **VPN** address, so this traffic went
through the tunnel and not over the public interface. The way in is proven
working before the way in over port 22 is taken away — the same ordering the
Control Plane used.

---

## 7. Join the cluster

The token minted by `kubeadm init` expires 24 hours after init, so it was
gone. A fresh one is minted on the Control Plane and used immediately:

```
ssh cp-dev@10.100.0.1 sudo kubeadm token create --print-join-command
```

It prints:

```
kubeadm join k8s-cp.tosak.internal:6443 --token <redacted> \
  --discovery-token-ca-cert-hash sha256:a4aab644…
```

**The token is not recorded here**, deliberately — it is a bootstrap
credential. The CA hash is a public fingerprint and is safe to keep.

Note what the command names: `k8s-cp.tosak.internal:6443`. Step 5's
`/etc/hosts` line is what makes it resolvable. This is the step that would
have failed without it.

The joins were run **over the VPN**, as `tosak` with `sudo`:

```
ssh tosak@10.100.0.2 sudo kubeadm join k8s-cp.tosak.internal:6443 …
ssh tosak@10.100.0.3 sudo kubeadm join …
ssh tosak@10.100.0.4 sudo kubeadm join …
```

Public port 22 was still open at this point, but was not used. Running the
join over the tunnel proves the rest of the build has no dependency on the
bootstrap port before that port is withdrawn.

All three reported:

```
This node has joined the cluster:
* Certificate signing request was sent to apiserver and a response was received.
* The Kubelet was informed of the new secure connection details.
```

---

## 8. Verify the cluster

### Nodes

```
kubectl get nodes -o wide
kubectl get nodes -o custom-columns='NAME:.metadata.name,PROVIDERID:.spec.providerID,TAINTS:.spec.taints[*].key'
```

| Node | Status | Version | InternalIP | ExternalIP | providerID | Taints |
|---|---|---|---|---|---|---|
| k8s-cp | Ready | v1.37.0 | 10.0.1.5 | 46.62.209.249 | `hcloud://166646798` | `node-role.kubernetes.io/control-plane` |
| k8swk1 | Ready | v1.37.0 | 10.0.2.6 | 135.181.154.56 | `hcloud://166652126` | none |
| k8swk2 | Ready | v1.37.0 | 10.0.2.7 | 62.238.56.128 | `hcloud://166652125` | none |
| k8swk3 | Ready | v1.37.0 | 10.0.2.8 | 2.29.31.80 | `hcloud://166652124` | none |

Every InternalIP is the **private** address, which is what `--node-ip` is for.
Every Worker already carries a `providerID` and **no**
`node.cloudprovider.kubernetes.io/uninitialized` taint: the CCM saw each node
and initialised it within seconds of the join. Nothing had to be prompted.

### Flannel bound the right interface on every node

```
kubectl logs -n kube-flannel <pod> -c kube-flannel | grep 'Using interface'
```

```
k8swk1  Using interface with name enp7s0 and address 10.0.2.6
k8swk2  Using interface with name enp7s0 and address 10.0.2.7
k8swk3  Using interface with name enp7s0 and address 10.0.2.8
k8s-cp  Using interface with name enp7s0 and address 10.0.1.5
```

The `--iface-regex=^10\.0\.` pin holds on Workers as well as on the Control
Plane. Without it flannel binds the public NIC and pod traffic leaves the
private network — silently.

Pod subnets: `10.244.0.0/24` on the Control Plane, then `10.244.1/2/3.0/24`.

### The MTU chain, end to end

These are the values of this build. Since 2026-11-22 `Backend.MTU` is 1400 and
the chain from `FLANNEL_MTU` to the pods is 1350
([Change the pod MTU](#change-the-pod-mtu)); `enp7s0` stays at 1450.

| Layer | Value |
|---|---|
| Hetzner private interface `enp7s0` | 1450 |
| `core/cni/flannel.yaml` `Backend.MTU` | 1450 — the **underlay** |
| `/run/flannel/subnet.env` `FLANNEL_MTU` | 1400 |
| `flannel.1` and `cni0` | 1400 |
| Pod `eth0` | 1400 |

Identical on all four nodes. The Phase 2 correction — `Backend.MTU` is the
underlay and flannel subtracts the 50-byte VXLAN overhead itself — reproduces
from a clean start. A Worker built from this runbook needs no
`ip link delete flannel.1`.

### Pod-to-pod traffic across nodes

A throwaway `busybox:1.37` DaemonSet in a `netcheck` namespace, one pod per
Worker, deleted afterwards.

| Test | Result |
|---|---|
| ping k8swk1 → k8swk2, k8swk1 → k8swk3 | 0% loss |
| ping with a 1372-byte payload (1400 total, fills the MTU) | 0% loss |
| 4 MiB TCP pull k8swk1 → k8swk2 | ok, 0.25 s |
| 4 MiB TCP pull k8swk3 → k8swk2 | ok |
| `nslookup kubernetes.default.svc.cluster.local` | `10.96.0.1` |

The bulk TCP transfers matter more than the pings: an MTU mismatch usually
shows as a TCP black hole, where small packets pass and large ones vanish.
Both directions moved 4 MiB cleanly.

DNS resolving to `10.96.0.1` also confirms the narrowed service CIDR
`10.96.0.0/16` ([ADR 0001](../adr/0001-cluster-network-plan.md)). The `/12` default would have contained the
WireGuard range `10.100.0.0/24`.

BusyBox `ping` has no `-M do` flag, so a don't-fragment test was not possible
from inside the pod. The 1372-byte payload and the bulk transfers cover the
same ground.

### The Pending pod resolved itself

`hcloud-csi-controller` sat Pending through all of Phase 2 — it tolerates
neither `control-plane:NoSchedule` nor `uninitialized`, so it had nowhere to
go. It scheduled onto `k8swk1` seconds after the first join, with nothing
applied. `kube-system` now runs 16 pods, all Running:

```
coredns x2, etcd, kube-apiserver, kube-controller-manager, kube-scheduler,
hcloud-cloud-controller-manager, hcloud-csi-controller (5/5),
hcloud-csi-node x4 (3/3 each), kube-proxy x4
```

---

## 9. Close bootstrap SSH

`allow_public_ssh_worker` defaults false, so a plain plan closes it.

```
terraform -chdir=shared plan -out=worker-ssh-close.tfplan
terraform -chdir=shared apply worker-ssh-close.tfplan
```

```
Plan: 0 to add, 1 to change, 0 to destroy.
```

The plan printed `# (3 unchanged blocks hidden)` on a firewall whose
configuration declares one rule and no `apply_to`. Reading the plan JSON
showed what they are:

```
terraform -chdir=shared show -json worker-ssh-close.tfplan \
  | jq '.resource_changes[] | select(.address=="hcloud_firewall.worker")'
```

Three `apply_to` entries — servers `166652124`, `166652125`, `166652126` —
identical before and after. `apply_to` is Optional+Computed in the provider,
so a refresh fills it in from the API even though this module never names a
server; the Workers attached themselves through `firewall_ids` in the
`workers` module, as [ADR 0002](../adr/0002-control-plane-isolation.md) requires. **The attachment is not being torn
off.** The only real change is `rule: [one] → []`.

Which is exactly the update the provider cannot perform.

### 🔴 The apply lied, again, on schedule

Terraform completed without error. The API said otherwise:

```
curl -s -H "Authorization: Bearer $TF_VAR_HCLOUD_TOKEN" \
  https://api.hetzner.cloud/v1/firewalls | jq .
```

```
11651947 tosak-worker-firewall rules=1 [in tcp 22 185.100.244.43/32]
         applied_to=[166652124,166652125,166652126]
```

**Port 22 was open on all three Workers while Terraform's state said it was
closed.** This is the precise failure [ADR 0004](../adr/0004-kluster-safety-model.md) exists to prevent, and it was
caught only because the standing rule is to read the API rather than trust
apply output. Predicted in advance from the Control Plane's experience, and it
still happened exactly as written.

### Clearing it by hand

```
curl -s -X POST -H "Authorization: Bearer $TF_VAR_HCLOUD_TOKEN" \
  -H "Content-Type: application/json" -d '{"rules":[]}' \
  https://api.hetzner.cloud/v1/firewalls/11651947/actions/set_rules
```

```
set_firewall_rules success 100%
apply_firewall   running  10%     (x3, one per attached server)
```

The API accepts an empty rule set without complaint, so the provider is the
limit and not Hetzner. Note the second action: with servers attached, clearing
the rules also queues an `apply_firewall` per server. On the Control Plane this
step had no attached-server propagation to wait for; here there are three.

### Final verification

```
curl … /v1/firewalls | jq .
curl … /v1/servers   | jq .
nc -vz <address> 22
kubectl get --raw=/readyz
terraform -chdir=shared  plan -detailed-exitcode
terraform -chdir=workers plan -detailed-exitcode
```

| Check | Result |
|---|---|
| `tosak-cp-firewall` 10289761 | `in udp 51820 0.0.0.0/0`, nothing else |
| `tosak-worker-firewall` 11651947 | **no rules at all** |
| firewall status per server | `applied` on all four |
| `nc -vz … 22` on all four public addresses | closed / filtered |
| `ssh cp-dev@10.100.0.1` | works |
| `ssh tosak@10.100.0.2/.3/.4` | works |
| `kubectl get --raw=/readyz` | `ok` |
| `terraform -chdir=shared plan` | `No changes.`, exit code 0 |
| `terraform -chdir=workers plan` | `No changes.`, exit code 0 |

**Port 22 is closed cluster-wide.** The only port open to the world anywhere
in this cluster is UDP 51820 on the Control Plane.

---

## Phase 3 final state

| | |
|---|---|
| Nodes | `k8s-cp`, `k8swk1`, `k8swk2`, `k8swk3` — all `Ready`, all v1.37.0 |
| Node names | stable, no suffix; a rebuild reuses them |
| InternalIP | private network on every node |
| providerID | set by the CCM on every node |
| CNI | Flannel `10.244.0.0/16`, bound to `enp7s0`, pod MTU 1400 |
| Service CIDR | `10.96.0.0/16` |
| Storage | `hcloud-volumes` default; CSI controller on `k8swk1`, node plugin on all four |
| VPN | hub `10.100.0.1`, spokes `.2 .3 .4`, operator `.69` |
| Public exposure | UDP 51820 on the Control Plane only |
| `kube-system` | 16 pods, all Running |

---

## Appendix — workstation changes

These are on the operator's laptop, not in the cluster. Recorded because a
rebuild invalidates all of them and the next one will need the same edits.

| File | Change | Backup |
|---|---|---|
| `~/.ssh/known_hosts` | `ssh-keygen -R` for `10.100.0.2`, `.3`, `.4` — stale keys from the dead Workers | `known_hosts.bak-phase3-2026-09-20` |
| `~/.ssh/config` | `cp-authos` repointed from `46.62.209.249` to `10.100.0.1`; the dead `wk1-authos`/`wk2-authos` entries replaced by `k8swk1`/`k8swk2`/`k8swk3` on `10.100.0.2/.3/.4` as `tosak` | `config.bak-phase3-2026-09-20` |

`cp-authos` had to move because step 9 of the Control Plane runbook closed
port 22 on the public address, which turned a working entry into one that
hangs. **Any host alias pointing at a public address is invalidated by closing
bootstrap SSH**, not by the rebuild itself — so the breakage appears one phase
after the change that caused it.

Every alias now names a VPN address, which is the only way in.
