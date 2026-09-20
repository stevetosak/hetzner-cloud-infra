# Workers — build runbook

Written verbatim as each command ran, on 2026-09-20 (ADR 0004). This file is
the deliverable of Phase 3. Phase 6 ports it into `kluster`.

Read `docs/runbook/rebuild-2026-09-20.md` for the surrounding plan,
`docs/runbook/control-plane.md` for the Control Plane this joins to, and
`docs/adr/0006` for why the network settings are what they are.

**Conventions in this file.** A fenced block is a command that was run, exactly
as it was run. Output is quoted only where it was used as evidence. Every
claim about Hetzner state was checked against the API, never read from
Terraform output.

Environment for every Terraform command:

```
cd infra && source .envrc
```

## Why this was done by hand

`infra/scripts/bootstrap/bootstrap_workers.sh` and the `kluster` CLI both call
the same four-stage pipeline, and neither is usable as written. Five defects
were known or found before a command was run:

1. **`kubeadm join` would fail on every Worker.** `kubeadm token create
   --print-join-command` prints the control-plane endpoint
   `k8s-cp.tosak.internal:6443` (ADR 0006). No Worker resolves that name, and
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
afterwards. That is the order ADR 0004 asks for.

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
cannot be expressed by this module (ADR 0005).

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
ADR 0005 requires: Hetzner Volumes are location-bound, so a Worker must sit
where the PostgreSQL volumes are.

Workers carry **no** delete or rebuild protection, unlike the Control Plane.
That is deliberate (ADR 0002): a Worker is meant to be replaceable.

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
can reach the Worker (ADR 0006).

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
`--control-plane-endpoint` (ADR 0006). `.internal` is ICANN-reserved and this
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
never be open publicly (ADR 0006).

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
`10.96.0.0/16` (ADR 0001). The `/12` default would have contained the
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
`workers` module, as ADR 0002 requires. **The attachment is not being torn
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
closed.** This is the precise failure ADR 0004 exists to prevent, and it was
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
