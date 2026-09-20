# infra/ — Terraform

Three root modules, three state files, one Hetzner project.

| Module | Owns | State key |
|---|---|---|
| `shared/` | network `authos-net`, 4 subnets, firewall, ssh key, primary IP | `tfstate/shared.tfstate` |
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
2. `hcloud_firewall.authos_cluster_firewall` — only when `allow_public_ssh` is
   true, the port 22 `source_ips` re-pins from the address recorded at the last
   apply to the address of the machine running terraform now. The rule tracks
   your current ISP address by design; a changed value is the mechanism
   working. With `allow_public_ssh = false` the rule is removed instead.

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
