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
