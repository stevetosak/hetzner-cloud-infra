# infra/ — Terraform

Three root modules, three state files, one Hetzner project.

| Module | Owns | State key |
|---|---|---|
| `shared/` | network `tosak-net`, 4 subnets, firewall, ssh key, primary IP | `tfstate/shared.tfstate` |
| `control-plane/` | the server `k8s-cp`, and nothing else | `tfstate/control-plane.tfstate` |
| `workers/` | the worker map `k8swk1..3`, and nothing else | `tfstate/workers.tfstate` |

They are separate on purpose. One root module used to hold the control plane
and the workers together, so every worker operation computed a plan against
the control plane, and the only thing standing between the two was an operator
reading that plan carefully. On 2026-09-13 that failed and every server was
destroyed. See `docs/adr/0002-control-plane-isolation.md`.

Modules do not read each other's state. Each finds shared resources through
hcloud **data sources by name**. So `workers/` needs no credentials for the
shared backend, and cannot express the control plane even in principle.

**Names are interface.** Renaming a resource in `shared/` breaks the dependent
modules at plan time.

## Apply order

`shared/` → `control-plane/` → `workers/`.

## Environment

State lives in Cloudflare R2, not in git. Every command needs:

```sh
cd infra && source .envrc
```

`.envrc` is gitignored and must export three things:

```sh
export TF_VAR_HCLOUD_TOKEN=...   # Hetzner API token
export AWS_ACCESS_KEY_ID=...     # R2 access key id
export AWS_SECRET_ACCESS_KEY=... # R2 secret access key
```

**No AWS account is involved.** Terraform has no R2 backend, so R2 is reached
through the `s3` backend, which is built on the AWS SDK and reads credentials
from the standard AWS environment variables. The values are the ones Cloudflare
issued for the R2 API token; the dashboard labels them "Access Key ID" and
"Secret Access Key". Every AWS-specific step — IAM validation, the account-id
lookup, the EC2 metadata probe — is switched off in `backend.tf`.

**The two R2 names carry no `TF_VAR_` prefix.** That prefix supplies a
Terraform input `variable`, and a backend block cannot read variables at all,
so `TF_VAR_AWS_ACCESS_KEY_ID` is silently ignored and `init` fails to
authenticate. Only `TF_VAR_HCLOUD_TOKEN` takes the prefix, because it feeds a
real `variable` declaration.

## First-time setup of `shared/`

The network, its four subnets, the firewall, the ssh key and the primary IP all
**survived** the 2026-09-13 loss. They are imported, never created.

```sh
cd infra/shared
terraform init
./import.sh
terraform plan
```

`terraform import` is a state-only operation; it never changes Hetzner.

### What a correct `plan` looks like after the import

**Zero to create, zero to destroy, zero to replace.** Anything else means the
import did not match reality — stop and investigate rather than applying.

Two in-place updates are expected, and both are intended:

1. `hcloud_primary_ip.cp_authos_ip` — `delete_protection` false → true.
2. `hcloud_firewall.authos_cluster_firewall` — only when bootstrap SSH is
   open, the port 22 `source_ips` re-pins from the address recorded at the last
   apply to the address of the machine running terraform now. The rule tracks
   your current ISP address by design; a changed value is the mechanism
   working. When it is closed the rule is removed instead.

Both resource names above are the ones the import used. ADR 0007 has since
renamed them to `hcloud_firewall.cp` and `hcloud_primary_ip.cp`, through
`moved` blocks, so no Hetzner resource was touched.

## Bootstrap SSH

Port 22 is closed everywhere in steady state. Nothing on any public interface
accepts a connection except UDP 51820 on the Control Plane, which is the
WireGuard hub. Operators reach every server over the VPN.

A server can only be reached publicly while it is being built, because its
tunnel does not exist yet. Two variables open that window, one per firewall,
and both default to `false`:

| Variable | Firewall | Opened during |
|---|---|---|
| `allow_public_ssh_cp` | `tosak-cp-firewall` | Phase 2, until the hub is up |
| `allow_public_ssh_worker` | `tosak-worker-firewall` | Phase 3, until each Worker is peered |

```sh
terraform -chdir=infra/shared plan -out=open.tfplan -var allow_public_ssh_worker=true
terraform -chdir=infra/shared apply open.tfplan
```

Closing is the same command with the variable dropped, since the default is
`false`. Close it as soon as the tunnel works — ADR 0004 exists because the
previous cluster passed `allow_public_ssh=true` on every apply and never
revoked it.

They are two variables and not one because the two firewalls open at different
times. A single switch would have made closing the Control Plane's port also
close every Worker's, so building a Worker would have reopened a Control Plane
that no longer needs it.

The address is resolved at plan time from the machine running terraform, so a
changed ISP address appears as an in-place update. That is the mechanism
working, not drift.

### Always verify a close over the API

**hcloud provider 1.69.0 cannot take a firewall from one rule to none.** It
reports `Apply complete`, writes the empty rule set into state, and leaves the
rule standing at Hetzner. Every later plan then shows the same pending change
forever. Removing one rule out of several works; removing the last one does
not. 1.69.0 is the newest stable, so there is nothing to upgrade to.

The API itself is fine with an empty rule set, so the provider is the limit:

```sh
curl -s -X POST -H "Authorization: Bearer $TF_VAR_HCLOUD_TOKEN" \
  -H "Content-Type: application/json" -d '{"rules":[]}' \
  "https://api.hetzner.cloud/v1/firewalls/<id>/actions/set_rules"
```

This bites hardest when closing `allow_public_ssh_worker` after a Worker
build, because that is exactly a one-rule-to-none update, on a firewall
attached to running servers. Terraform will say the port is closed while it is
open — the ADR 0004 failure. **After any close, read the firewall back from the
API and clear it by hand if the rule survived:**

```sh
curl -s -H "Authorization: Bearer $TF_VAR_HCLOUD_TOKEN" \
  "https://api.hetzner.cloud/v1/firewalls/<id>" | jq '.firewall.rules'
```

Firewall ids: `tosak-cp-firewall` is `10289761`, `tosak-worker-firewall` is
`11651947`.

## Replacing the control plane

`control-plane/` is protected three ways, and all three must be cleared by
hand. This friction is deliberate — it is the whole point of ADR 0002, and it
is not a bug to route around.

1. Remove `lifecycle { prevent_destroy = true }` from
   `control-plane/main.tf` and commit that removal.
2. Set `delete_protection = false` and `rebuild_protection = false` on the same
   resource, and `terraform apply`. Hetzner enforces these server-side, so they
   hold against a stale state file and a rogue token; Terraform alone cannot
   bypass them.
3. Only now run the destroy or replace.
4. Put all three back afterwards, and verify with `terraform plan` that they
   are in force again.

The primary IP `46.62.209.249` is a separate resource in `shared/` with
`auto_delete = false`, so it survives a control-plane replacement and the SSH,
WireGuard and DNS endpoints do not change.

## Worker names are stable

`k8swk1`, `k8swk2`, `k8swk3`, forever. There is no `node_suffix` and there must
never be one again. A global suffix minted per CLI run renamed every worker at
once, and a name change forces server replacement — that destroyed every
database on 2026-09-13. The reason names had to change (Longhorn binding node
names to local disk UUIDs) is gone with Longhorn. See
`docs/adr/0005-storage-and-worker-identity.md`.

Because names are reused, a recreated worker must have its old Kubernetes Node
object deleted before it rejoins, or it inherits stale taints and labels.
