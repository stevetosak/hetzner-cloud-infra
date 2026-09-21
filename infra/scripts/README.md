# infra/scripts

**These scripts are superseded and are not the procedure.** The procedures are
the runbooks:

| Deliverable | Covers |
|---|---|
| `docs/runbook/control-plane.md` | Building the Control Plane, Phase 2 |
| `docs/runbook/workers.md` | Building and joining a Worker, Phase 3 |

Both were written verbatim as their commands ran (ADR 0004). The Control Plane
and all three Workers of the current cluster were built from them by hand.
Nothing here was run.

## Why these scripts were not used

`bootstrap_workers.sh` and the four stages under `pipeline/` carry five
defects, three of them found while writing `docs/runbook/workers.md`. Read the
"Why this was done by hand" section at the head of that file for the full
account. In summary:

1. **No Worker would join.** The join command names the Control Plane Endpoint
   `k8s-cp.tosak.internal:6443` (ADR 0006), and nothing here writes the
   `/etc/hosts` line that resolves it.
2. **`bootstrap_workers.sh` would destroy the WireGuard hub configuration.** It
   rebuilds the Control Plane's `wg0.conf` from a template, discarding the hub
   config and the operator's own peer. Peers must be appended.
3. It reads `$HOME/k8s/infra/stage/out/worker_ips.txt`, a path that does not
   exist, and assigns VPN addresses by counting lines in it. Worker identity is
   declared per name in `infra/workers/terraform.tfvars` and exposed through
   the `worker_vpn_ips` output.
4. **The CNI plugin pin has never worked.** Stage 3 extracts a tarball into
   `/opt/cni/bin`; stage 4's `kubelet` pulls in `kubernetes-cni`, which
   overwrites it and becomes the dpkg owner. `kubernetes-cni` is also absent
   from the `apt-mark hold` set.
5. **`adduser --disabled-password` plus the `sudo` group gives an unusable
   sudo.** No password exists to answer the prompt; a `NOPASSWD` sudoers
   drop-in is required.

A sixth problem is not a script defect but breaks them anyway: the private
network attach races cloud-init, so `enp7s0` is sometimes unconfigured. Stage 4
exits when it cannot read an address there, which made this an intermittent,
luck-dependent failure. The runbook writes a netplan drop-in to remove the
race.

**They were left unrepaired on purpose.** Editing a script that is not run
makes it look maintained, and the next real run would still be the first run of
unproven code. Phase 6 ports the runbooks into `kluster`, at which point these
scripts are either rewritten from the runbooks or deleted.

## Deleted in Phase 3

- `bootstrap/reset-nodes.sh` — passed `-var="allow_public_ssh=true"` and
  `-var="node_suffix=…"`, neither of which exists any more, and read a path
  that does not exist. Destructive and dead.
- `bootstrap/pipeline/bootstrap_node-5-longhorn.sh` and
  `utils/longhorn_config.sh` — Longhorn is gone (ADR 0005). Postgres uses
  Hetzner Cloud Volumes through the hcloud CSI driver.
- `infra/out/worker_ips.txt` and `infra/out/worker_names.txt` — held the dead
  Workers' public addresses and their suffixed names. Replaced by the
  `worker_vpn_ips`, `worker_private_ips` and `worker_public_ips` outputs, which
  are keyed by name rather than by position.
