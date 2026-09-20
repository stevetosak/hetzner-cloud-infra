# Control plane — build runbook

Written verbatim as each command ran, on 2026-09-20 (ADR 0004). This file is
the deliverable of Phase 2. Phase 6 ports it into `kluster cp init` stages.

Read `docs/runbook/rebuild-2026-09-20.md` for the surrounding plan, and
`docs/adr/0006` for why the network settings are what they are.

**Conventions in this file.** A fenced block is a command that was run, exactly
as it was run. Output is quoted only where it was used as evidence. Every
claim about Hetzner state was checked against the API, never read from
Terraform output.

Environment for every Terraform command:

```
cd infra && source .envrc
```

That exports `TF_VAR_HCLOUD_TOKEN`, `AWS_ACCESS_KEY_ID` and
`AWS_SECRET_ACCESS_KEY`. The two R2 names must **not** carry a `TF_VAR_`
prefix — a backend block cannot read Terraform variables.

---

## 1. Create the server

```
terraform -chdir=infra/control-plane init
terraform -chdir=infra/control-plane plan -out=cp.tfplan
terraform -chdir=infra/control-plane apply cp.tfplan
```

The plan was saved to a file and reviewed before the apply, so what ran is what
was read. The plan was:

```
Plan: 1 to add, 0 to change, 0 to destroy.
```

Zero destroy and zero replace is the gate. The apply reported:

```
Apply complete! Resources: 1 added, 0 changed, 0 destroyed.

id         = "166646798"
name       = "k8s-cp"
private_ip = "10.0.1.5"
public_ip  = "46.62.209.249"
```

### Verified over the Hetzner API

```
curl -s -H "Authorization: Bearer $TF_VAR_HCLOUD_TOKEN" \
  https://api.hetzner.cloud/v1/servers/166646798 | jq .
```

| Property | Value |
|---|---|
| id / name | `166646798` / `k8s-cp` |
| status | `running` |
| type / image / location | `cx23` / `ubuntu-24.04` / `hel1` |
| public IPv4 | `46.62.209.249` (primary IP `110296483`) |
| IPv6 | none — disabled deliberately |
| private | network `11736362`, `10.0.1.5` |
| firewall | `10289761` `tosak-cp-firewall`, status `applied` |
| protection | `delete: true`, `rebuild: true` |
| labels | `role=control-plane` |

The primary IP now reports `assignee_id = 166646798`, and the firewall now
reports the same server in `applied_to`. The project holds exactly one server.

The surviving primary IP was reused, so the SSH and WireGuard endpoint is the
same address as before the loss. Nothing outside this module was touched.

---

## 2. Base host setup

Run as `root` over SSH. The Hetzner image injects the cluster SSH key into
`root` only — `cp-dev` does not exist yet, so the first login of a rebuilt
control plane is always `root`.

```
ssh -i ~/.ssh/hetzner-cluster root@46.62.209.249
```

The readback that established the starting point:

| | |
|---|---|
| hostname | `k8s-cp` |
| OS / kernel | Ubuntu 24.04.4 LTS / 6.8.0-138-generic |
| public interface | `eth0`, `46.62.209.249/32` |
| **private interface** | **`enp7s0`, `10.0.1.5/32`** |
| non-system users | none |
| swap | none |

`enp7s0` is load-bearing. It is the interface the kubelet `--node-ip` and the
Flannel `--iface-regex` both select, and it is what makes ADR 0006's
three-interface rule hold. A future image that names it differently breaks
both, silently.

This stage follows `infra/scripts/bootstrap/pipeline/bootstrap_node-1-user-sysctl.sh`
and `-3-containerd.sh`, with two deliberate differences: the user is `cp-dev`,
not the workers' `tosak`; and the containerd unit file is fetched from tag
`v2.2.0` rather than `main`. The tagged and `main` copies were byte-identical
on the day, so pinning cost nothing and removes a moving dependency.

The whole stage ran under `set -euo pipefail`, so a failure stops the host
half-built rather than continuing past it.

```
# user
adduser --disabled-password --gecos '' cp-dev
usermod -aG sudo cp-dev
install -d -m 700 -o cp-dev -g cp-dev /home/cp-dev/.ssh
cp /root/.ssh/authorized_keys /home/cp-dev/.ssh/authorized_keys
chmod 600 /home/cp-dev/.ssh/authorized_keys
chown cp-dev:cp-dev /home/cp-dev/.ssh/authorized_keys
echo 'cp-dev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/cp-dev
chmod 440 /etc/sudoers.d/cp-dev
visudo -c

# kernel modules and sysctl
printf 'overlay\nbr_netfilter\n' > /etc/modules-load.d/k8s.conf
modprobe overlay
modprobe br_netfilter
cat > /etc/sysctl.d/k8s.conf <<EOF
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
EOF
sysctl --system

# swap
swapoff -a
sed -i '/ swap / s/^/#/' /etc/fstab

# packages and time sync
apt-get update
apt-get install -y chrony curl gpg apt-transport-https ca-certificates wget
systemctl enable --now chrony

# containerd 2.2.0
wget -qO- https://github.com/containerd/containerd/releases/download/v2.2.0/containerd-2.2.0-linux-amd64.tar.gz | tar -C /usr/local -xz
mkdir -p /usr/local/lib/systemd/system
curl -fsSL https://raw.githubusercontent.com/containerd/containerd/v2.2.0/containerd.service \
  -o /usr/local/lib/systemd/system/containerd.service
mkdir -p /etc/containerd
containerd config default > /etc/containerd/config.toml
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml
systemctl daemon-reload
systemctl enable --now containerd

# runc 1.4.0 and CNI plugins 1.9.0
cd /tmp
wget -q https://github.com/opencontainers/runc/releases/download/v1.4.0/runc.amd64
install -m 755 runc.amd64 /usr/local/sbin/runc
mkdir -p /opt/cni/bin
wget -q https://github.com/containernetworking/plugins/releases/download/v1.9.0/cni-plugins-linux-amd64-v1.9.0.tgz
tar -C /opt/cni/bin -xzf cni-plugins-linux-amd64-v1.9.0.tgz
```

### Why `cp-dev` gets a sudoers drop-in

`adduser --disabled-password` plus membership of `sudo` does **not** give a
usable `sudo`: the account has no password, so the prompt cannot be answered.
The worker pipeline has the same defect, and Phase 3 should fix it the same
way. The drop-in is safe here because the only route in is the SSH key, and
the firewall admits port 22 from the operator workstation alone.

### Verified

```
containerd --version            v2.2.0
runc --version                  1.4.0
ls /opt/cni/bin | wc -l         20 plugins
systemctl is-active containerd  active     (and enabled)
grep -c 'SystemdCgroup = true'  1
lsmod                           overlay, br_netfilter both loaded
sysctl                          bridge-nf-call-iptables=1, -ip6tables=1, ip_forward=1
swapon --show                   empty; no uncommented swap line in /etc/fstab
chronyc tracking                synchronised to time.cloudflare.com, 119 ns fast
id cp-dev                       uid=1000, groups include sudo
sudo -u cp-dev sudo -n true     succeeds
```

And the operator's own route now works, which is the real test:

```
$ ssh cp-authos 'whoami; hostname; sudo -n id -un'
cp-dev
k8s-cp
root
```

### Known noise

`apt` emitted `perl: warning: Setting locale failed` for several `mk_MK.UTF-8`
variables. SSH forwards the workstation's `LC_*` settings, and the server has
no Macedonian locale installed. It is cosmetic. Prefix a command with `LC_ALL=C`
to silence it.

---

## 3. WireGuard hub

The Control Plane is the VPN hub **and its router**: an operator reaches a
Worker because the hub forwards between two of its own peers (ADR 0006). The
`net.ipv4.ip_forward = 1` set in step 2 is what makes that true, and the
`FORWARD` policy was confirmed `ACCEPT` before relying on it.

Only the hub accepts an inbound WireGuard port. The hub has a `ListenPort` and
its peers have no `Endpoint` — it learns each peer's address from that peer's
first packet. This is why no Worker ever needs inbound UDP 51820, and why
`tosak-worker-firewall` carries TCP 22 and nothing else.

The laptop's public key is read from `tools/kluster/kluster.yaml`, which is the
committed record of this topology. The operator's existing keypair is reused.

```
apt-get install -y wireguard
mkdir -p /etc/wireguard
umask 077
wg genkey | tee /etc/wireguard/private.key | wg pubkey > /etc/wireguard/public.key
chmod 600 /etc/wireguard/private.key

cat > /etc/wireguard/wg0.conf <<EOF
[Interface]
Address    = 10.100.0.1/24
ListenPort = 51820
PrivateKey = $(cat /etc/wireguard/private.key)

# Worker peers are added in Phase 3, once each worker has generated its key.
# Their VPN addresses are declared in infra/workers/terraform.tfvars:
#   k8swk1 10.100.0.2 / k8swk2 10.100.0.3 / k8swk3 10.100.0.4

[Peer]
# admin-laptop
PublicKey  = 7v0/KwZNn08j5rtN8IQ2C8f9kOeuEqKEIlWwr4Qhq00=
AllowedIPs = 10.100.0.69/32
EOF
chmod 600 /etc/wireguard/wg0.conf

systemctl enable --now wg-quick@wg0
```

**The hub generates a new private key, so its public key is new.** A rebuild
always invalidates every spoke config. The key produced on this run was:

```
Cy2AjXYE8BHjoPJkDocHywi9ZYMADOljW5qvKUIUngk=
```

That value is not a secret and is not reusable — the next rebuild prints a
different one. It is recorded here only so the sequence reads honestly.

### The operator's own config

Done by the operator, on the laptop, with their own sudo:

```
sudo cp /etc/wireguard/wg0.conf /etc/wireguard/wg0.conf.bak-2026-09-20
sudo sed -i 's|^PublicKey.*=.*|PublicKey = <new hub public key>|' /etc/wireguard/wg0.conf
sudo systemctl restart wg-quick@wg0
```

The `Endpoint` line does **not** change. The surviving primary IP was reused,
so the hub is still reachable at `46.62.209.249:51820`. That is one of the
reasons the primary IP is delete-protected and managed in `shared/`.

### Verified from both ends

On the hub:

```
peer: 7v0/KwZNn08j5rtN8IQ2C8f9kOeuEqKEIlWwr4Qhq00=
  endpoint: 185.100.244.43:59643
  allowed ips: 10.100.0.69/32
  latest handshake: 45 seconds ago
  transfer: 212 B received, 92 B sent
```

`ping` succeeded in both directions with 0% loss: hub to `10.100.0.69`, and
laptop to `10.100.0.1`.

Then the route that matters, because port 22 closes at the end of this phase:

```
$ ssh cp-dev@10.100.0.1 'whoami; hostname; echo $SSH_CONNECTION; sudo -n id -un'
cp-dev
k8s-cp
server saw client 10.100.0.69
root
```

The server saw the client as `10.100.0.69`, so that session rode the tunnel
rather than the public path. The operator route is proven before the public
route is withdrawn.

---

## 4. Prepare for `kubeadm init`

Three settings must be right **before** `init`. Each one is silent if it is
wrong: the cluster comes up, and something fails much later for a reason that
does not point back here. That is why this is its own step.

```
# Kubernetes v1.37 packages
mkdir -p /etc/apt/keyrings
curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.37/deb/Release.key \
  | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.37/deb/ /' \
  > /etc/apt/sources.list.d/kubernetes.list
apt-get update
apt-get install -y kubelet kubeadm kubectl
apt-mark hold kubelet kubeadm kubectl

# the Control Plane Endpoint (ADR 0006)
echo '10.0.1.5  k8s-cp.tosak.internal' >> /etc/hosts

# kubelet: private address, and external cloud provider
echo 'KUBELET_EXTRA_ARGS=--node-ip=10.0.1.5 --cloud-provider=external' > /etc/default/kubelet
systemctl daemon-reexec
systemctl enable kubelet
```

| Setting | What breaks without it |
|---|---|
| the `/etc/hosts` line | `kubeadm init` cannot resolve its own `--control-plane-endpoint` and fails outright. `.internal` is ICANN-reserved and deliberately absent from public DNS. |
| `--node-ip=10.0.1.5` | The node advertises its public address and cluster traffic leaves the Private Network. One of the six settings in ADR 0006. |
| `--cloud-provider=external` | The node never gets a `providerID`, so the CSI driver can never attach a volume. The worker pipeline sets this; nothing set it for the control plane. |

### Verified

```
kubeadm version          v1.37.0
kubelet --version        v1.37.0
kubectl gitVersion       v1.37.0
apt-mark showhold        kubeadm, kubectl, kubelet
getent hosts k8s-cp.tosak.internal
                         10.0.1.5   k8s-cp.tosak.internal
/etc/default/kubelet     KUBELET_EXTRA_ARGS=--node-ip=10.0.1.5 --cloud-provider=external
systemctl is-enabled kubelet    enabled
systemctl is-active  kubelet    inactive
systemctl is-active  containerd active
```

`kubelet` being enabled but not active is correct at this point. It has no
configuration until `init` writes one.

### Finding — the CNI plugin pin is not real

`kubelet` pulls in `kubernetes-cni` as a dependency, here `1.9.1-1.1`, and that
package installs into `/opt/cni/bin`. After this step `dpkg -S` reports
**`kubernetes-cni`** as the owner of `/opt/cni/bin/bridge`, and the directory's
mtime is this step's, not step 2's.

So the CNI plugins that step 2 pinned to `1.9.0` by hand were replaced by
`1.9.1` one stage later. The worker pipeline orders its stages the same way
(`bootstrap_node-3-containerd.sh` then `-4-kubernetes.sh`), so it has always
done this too. The pin has never taken effect.

Nothing here is broken — 1.9.1 is fine, and Flannel ships its own plugin
binary. Two things follow, both for Phase 3:

- The manual CNI download in `bootstrap_node-3-containerd.sh` is dead weight.
  Either drop it and let `kubernetes-cni` own the plugins, or re-extract after
  the package install so the pin means something.
- `kubernetes-cni` is **not** in the `apt-mark hold` set, so a later
  `apt upgrade` can move the plugin set again with nothing recording it.

---

## 5. `kubeadm init`

Run a dry run first. It costs nothing and it renders every certificate and
manifest into a temporary directory without touching the host.

`kubeadm init phase preflight` does **not** accept these flags — it takes only
a small subset, and rejects `--control-plane-endpoint` outright. Use
`--dry-run` on the full command instead.

```
kubeadm init --dry-run \
  --control-plane-endpoint=k8s-cp.tosak.internal:6443 \
  --apiserver-advertise-address=10.0.1.5 \
  --pod-network-cidr=10.244.0.0/16 \
  --service-cidr=10.96.0.0/16 \
  --apiserver-cert-extra-sans=10.100.0.1,46.62.209.249
```

The dry run reported the serving certificate it would sign, which is the thing
worth reading before committing:

```
[certs] apiserver serving cert is signed for DNS names
        [k8s-cp k8s-cp.tosak.internal kubernetes kubernetes.default
         kubernetes.default.svc kubernetes.default.svc.cluster.local]
        and IPs [10.96.0.1 10.0.1.5 10.100.0.1 46.62.209.249]
```

`10.96.0.1` proves the narrowed service CIDR took. `10.100.0.1` proves the
operator's VPN route is in the certificate. Then remove the dry-run directory
and run it for real:

```
rm -rf /etc/kubernetes/tmp
kubeadm init \
  --control-plane-endpoint=k8s-cp.tosak.internal:6443 \
  --apiserver-advertise-address=10.0.1.5 \
  --pod-network-cidr=10.244.0.0/16 \
  --service-cidr=10.96.0.0/16 \
  --apiserver-cert-extra-sans=10.100.0.1,46.62.209.249
```

| Flag | Why |
|---|---|
| `--control-plane-endpoint` | Without it a second control plane is impossible without a rebuild. It was absent from the original plan (ADR 0006). |
| `--apiserver-advertise-address=10.0.1.5` | The API server binds the Private Network, not the Public Interface. |
| `--pod-network-cidr=10.244.0.0/16` | Must match Flannel (ADR 0001). |
| `--service-cidr=10.96.0.0/16` | Narrowed from the `/12` default, which **contains** the VPN range `10.100.0.0/24` — a latent API-server hijack (ADR 0001). |
| `--apiserver-cert-extra-sans` | `10.100.0.1` is the operator route. `46.62.209.249` is break-glass only; port 6443 stays closed to the world. kubeadm adds the endpoint name itself. |

### The join command is deliberately not recorded here

`init` printed a bootstrap token and a CA hash. **The token value is not
written into this repository.** It expires 24 hours after `init`, so it would
be a stale secret in git within a day and useless in every later rebuild.

Phase 3 mints a fresh one on the day:

```
kubeadm token create --print-join-command
```

### Verified

```
kubectl get nodes
k8s-cp   NotReady   control-plane   v1.37.0   10.0.1.5   containerd://2.2.0

taints      node-role.kubernetes.io/control-plane=NoSchedule
            node.cloudprovider.kubernetes.io/uninitialized=NoSchedule
            node.kubernetes.io/not-ready=NoSchedule
providerID  (empty)

kube-system  etcd, kube-apiserver, kube-controller-manager,
             kube-scheduler, kube-proxy      all 1/1 Running
             coredns x2                      0/1 Pending

kubeadm-config  controlPlaneEndpoint: k8s-cp.tosak.internal:6443
                podSubnet:            10.244.0.0/16
                serviceSubnet:        10.96.0.0/16
node podCIDR    10.244.0.0/24

kubectl get --raw=/readyz     ok
```

**Three things look wrong here and are correct.** Do not act on any of them.

- `NotReady` — there is no pod network until step 7.
- `node.cloudprovider.kubernetes.io/uninitialized` and the empty `providerID` —
  the CCM has not run yet. It is cleared in step 8.
- CoreDNS `Pending` — the control plane keeps its `NoSchedule` taint and there
  are no Workers. CoreDNS and, later, the CSI controller stay `Pending` for the
  whole of Phase 2. Untainting the control plane to "fix" this is wrong.

`kube-controller-manager` read `0/1 Running` at 23 seconds old and was `1/1` at
four minutes. That was startup timing, not a fault.

---

## 6. Kubeconfig on the operator workstation

**The old context collides exactly.** The dead cluster's entry was also named
`kubernetes`, with user `kubernetes-admin`, context `kubernetes-admin@kubernetes`,
**and the same server address `https://10.100.0.1:6443`** — the VPN address is
reused, so the address alone does not distinguish them. Its CA and client
certificate are the dead cluster's and cannot work against the new one. A
plain merge would have silently overwritten a context while leaving its name
looking correct.

So every entity is renamed before the merge:

```
ssh cp-dev@10.100.0.1 'sudo cat /etc/kubernetes/admin.conf' > admin.conf
chmod 600 admin.conf

# cluster -> tosak, user -> tosak-admin, server -> https://10.100.0.1:6443
kubectl --kubeconfig=admin.conf config rename-context \
        kubernetes-admin@kubernetes tosak-admin@tosak

cp ~/.kube/config ~/.kube/config.bak-2026-09-20
KUBECONFIG=~/.kube/config:admin.conf kubectl config view --flatten > merged
install -m 600 merged ~/.kube/config
rm -f admin.conf merged
```

kubeadm writes the Endpoint name into `admin.conf`. The workstation
deliberately does not resolve `.internal`, so the server line is rewritten to
the VPN address, which is in the certificate (ADR 0006).

`admin.conf` is a `cluster-admin` credential. It was held only in a temporary
directory and deleted after the merge.

### Verified

Both contexts are present, and the old one is untouched, including its
`argocd` default namespace:

```
CURRENT   NAME                          CLUSTER      AUTHINFO
*         kubernetes-admin@kubernetes   kubernetes   kubernetes-admin   argocd
          tosak-admin@tosak             tosak        tosak-admin
```

The new context was proven to answer **before** `current-context` moved:

```
kubectl --context=tosak-admin@tosak get nodes     →  k8s-cp
kubectl config use-context tosak-admin@tosak
```

---

## 7. Flannel

```
kubectl apply -f core/cni/flannel.yaml --dry-run=server    # see the note below
kubectl apply -f core/cni/flannel.yaml
```

**The server dry run reports three false errors.** `namespaces "kube-flannel"
not found`, once per namespaced object. A server dry run never really creates
the namespace, so the objects that live in it have nothing to validate
against. It is an artefact of dry-run ordering, not a fault in the manifest.

The node reaches `Ready` about twenty seconds later, because a CNI finally
exists. The `node.kubernetes.io/not-ready` taint clears with it.

### Verified — the interface pin

This is the setting that matters most, and the one that is silent when wrong:

```
I0920 19:26:41 match.go:269] Using interface with name enp7s0 and address 10.0.1.5
```

`enp7s0` is the Private Network interface. Had `--iface-regex=^10\.0\.` been
missing, flannel would have bound the public NIC and every pod packet would
have left the private network (ADR 0006).

```
ip -d link show flannel.1
   vxlan id 1 local 10.0.1.5 dev enp7s0 dstport 8472
```

### Correction — the MTU was set twice

The manifest originally carried `Backend.MTU = 1400`, matching the number in
ADR 0001. The cluster came up with `FLANNEL_MTU=1350`.

**Flannel treats `Backend.MTU` as the underlay MTU and subtracts the 50-byte
VXLAN overhead itself.** Writing 1400 subtracts twice: 1400 − 50 = 1350. The
manifest now carries `1450`, the real MTU of the Hetzner private network, and
pods get the 1400 that ADR 0001 specifies. The header comment says so, because
1400 is the number a reader will expect to find there.

Re-applying the ConfigMap and restarting the DaemonSet was **not enough**:

```
FLANNEL_MTU=1400          # correct, and what each pod interface gets
flannel.1  mtu 1350       # WRONG, the device was not rebuilt
```

Flannel does not resize an existing VXLAN device. The restart reused the one
created with the old value, leaving pods on 1400-byte interfaces behind a
1350-byte tunnel. The device must be removed so flannel rebuilds it:

```
ip link delete flannel.1
kubectl -n kube-flannel rollout restart ds/kube-flannel-ds
```

Safe here because no workload uses the pod network yet — the control-plane
static pods are all on host networking. **After Workers join, this is no longer
a free operation.** Settle the MTU before Phase 3.

Final state:

```
FLANNEL_MTU=1400
6: flannel.1: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1400
   vxlan id 1 local 10.0.1.5 dev enp7s0
```

### Still expected, still not a fault

```
k8s-cp   Ready   control-plane

taints   node-role.kubernetes.io/control-plane=NoSchedule
         node.cloudprovider.kubernetes.io/uninitialized=NoSchedule

coredns x2   Pending
```

The `uninitialized` taint is the CCM's to clear, not Flannel's. CoreDNS stays
`Pending` for the rest of this phase.

---

## Appendix — operator workstation repairs

Not control-plane state. A rebuild onto a clean workstation would meet only
the second of these.

**The host key changed.** `~/.ssh/known_hosts` still held the **dead** control
plane's keys for `46.62.209.249`, so SSH refused with a changed-identity
error. The new host presents `SHA256:58iiV54MDXGf5wKb8m1uP8tqwltJoyhE3gBQ8SEuwjM`
(ed25519).

```
ssh-keygen -R 46.62.209.249       # backup kept at ~/.ssh/known_hosts.old
```

Hetzner exposes no host key over its API, so this fingerprint could not be
confirmed independently. It was accepted on first use, narrowed by the
firewall admitting port 22 from `185.100.244.43/32` alone. The boot log on the
Hetzner web console prints the fingerprint if a stricter check is ever wanted.

**A typo in the identity path.** `~/.ssh/config` pointed `cp-authos`,
`wk1-authos` and `wk2-authos` at `~/.ssh/hetzner_cluster`; the file is
`~/.ssh/hetzner-cluster`, with a hyphen. The key itself was correct — its
public half byte-matches `infra/shared/identity.tf`.

```
sed -i 's|hetzner_cluster|hetzner-cluster|g' ~/.ssh/config
```

**The VPN address had a stale key too.** The same repair was needed for
`10.100.0.1` once the tunnel came up. The fingerprint the host offered was
`SHA256:58iiV54MDXGf5wKb8m1uP8tqwltJoyhE3gBQ8SEuwjM` — identical to the key
already accepted on `46.62.209.249`. The same host answering on two
independent addresses is the confirmation the first acceptance lacked.

```
ssh-keygen -R 10.100.0.1
```
