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
