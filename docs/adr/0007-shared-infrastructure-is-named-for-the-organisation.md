# 7. Shared infrastructure is named for the organisation, not for an application

Date: 2026-09-20

## Status

Accepted.

## Context

The cluster hosts four applications — **authos**, **doma**, **wasteio**,
**imaps** — but its shared infrastructure was named after the first one. The
network was `authos-net`, the firewall `authos-cluster-firewall`, the ssh key
`authos-cluster`, the control plane's address `cp-authos-ip`, the load balancer
`authos-lb`, and the shared PostgreSQL cluster `authos-pg-cluster` with every
database owned by a role called `authos`.

So `doma`, `wasteio` and `imaps` each held a connection string naming a fourth
application, and the firewall protecting every server was named for one tenant.
The names described the cluster's history rather than its shape.

The correction was made during the rebuild because **that was the only cheap
window.** Zero servers existed. The load balancer was already scheduled for
deletion and rebuild in Phase 4, the PostgreSQL cluster did not exist and Phase
4 recreates it, and the secrets carrying the database username did not exist
and Phase 5 recreates them. Every one of these names was text in a repository.
Afterwards they are live resources with data behind them, and renaming the role
in particular becomes an `ALTER ROLE` against a running database coordinated
with a rewrite of every application's credentials.

## Decision

**Shared infrastructure carries the organisation name, `tosak`. An application
names only what is its own.**

| Was | Is |
|---|---|
| `authos-net` | `tosak-net` |
| `authos-cluster-firewall` | `tosak-cp-firewall`, plus a new `tosak-worker-firewall` (ADR 0006) |
| `authos-cluster` (ssh key) | `tosak-cluster` |
| `cp-authos-ip` | `tosak-cp-ip` |
| `authos-lb` | `tosak-lb` |
| `authos-pg-cluster` | `tosak-pg-cluster` |
| PostgreSQL role `authos` | PostgreSQL role `tosak` |

Unchanged, because they genuinely are Authos: the `authos` namespace, its
ArgoCD project, its ingress hosts `authos.tosak.net` / `authos-api.tosak.net` /
`authos-demo.tosak.net`, its config maps, and **the database `authos`** — which
is the application's own database, unlike the role that owns it.

Terraform resource labels were renamed with the resources, so the code and the
cloud agree: `hcloud_network.cluster`, `hcloud_firewall.cluster`,
`hcloud_ssh_key.cluster`, `hcloud_primary_ip.cp`, and the subnets `cp`,
`worker`, `reserved`, `lb`. Each carries a `moved` block, making every rename a
state move rather than a replacement.

## Consequences

- **Hetzner IDs did not change.** Names there are mutable labels, so nothing
  was destroyed or recreated. Anything written before 2026-09-20 uses the old
  names, and the correspondence is by ID — the table in
  `docs/runbook/rebuild-2026-09-20.md` records both.
- `control-plane/` and `workers/` find shared resources by name, so the rename
  had to land in one commit across all three modules. ADR 0002 designed that to
  fail at plan time, which is how a missed reference surfaces.
- The CCM's `hcloud` Secret carries `network=tosak-net`. It must be created
  after `shared/` is applied, or the CCM looks for a network that is not there
  yet.
- The PostgreSQL role name reaches into secret **values**, not just manifests.
  A mistake there fails at application startup rather than at plan time, which
  is quieter than every other rename in this decision. Phase 5 verifies by
  confirming each host serves.
