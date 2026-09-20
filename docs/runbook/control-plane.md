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
