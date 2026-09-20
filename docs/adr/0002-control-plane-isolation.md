# 2. Isolate the control plane from worker operations

Date: 2026-09-20

## Status

Accepted

## Context

A single `kluster node add` run destroyed every server in the cluster,
control plane included, taking all Longhorn data and four PostgreSQL
databases with it. The Hetzner action log shows the control plane deleted
first, six seconds ahead of any worker.

The exact trigger for the control plane's deletion was never proven. The
recorded control-plane attributes matched `main.tf` exactly, so no
configuration diff explains it. A CLI bug, a configuration drift, and an
operator approving a destructive plan all remained possible.

That ambiguity turned out not to matter, because all three share the same
countermeasures. What made any of them fatal was structural:

- One root module and one state file held the control plane and the workers
  together, so every worker operation computed a plan against the control
  plane.
- The only thing standing between a worker operation and the control plane
  was the operator reading the plan carefully.
- `infra/terraform.tfstate` was committed to git. Mid-incident, a
  `git restore .` silently rolled state back to a version describing servers
  that no longer existed, and the next apply ran against that.
- Nothing in the system could rebuild a control plane, so its survival was
  load-bearing. See CONTEXT.md on "Node" for how the vocabulary hid this.

## Decision

Four layers, none of which relies on an operator reading a plan carefully.

**1. Three root modules, separate state.** `shared/` owns the network,
subnets, firewall, ssh key and primary IP. `control-plane/` owns only the
control-plane server. `workers/` owns only the worker map. Each reaches
shared resources through hcloud data sources **by name**, not through
`terraform_remote_state` — so no module needs another module's state or
backend credentials, and the workers module cannot express the control plane
even in principle.

Three rather than two, keeping `shared/` apart from `control-plane/`,
because control-plane rebuilds become routine once automated, and a routine
destroy-and-create plan must not contain the network and subnets.

**2. Provider-side guard.** `lifecycle { prevent_destroy = true }` on the
control-plane server.

**3. Hetzner-side guard.** `delete_protection = true` and
`rebuild_protection = true` on the control-plane server. These are enforced
by the Hetzner API, not by Terraform, so they hold against a destroy, a
stale state file, a rogue token, and any future CLI bug. Every
`delete_server` call in the incident log would have been refused.

**4. State leaves git.** State moves to Cloudflare R2 via the `s3` backend
with `use_lockfile = true`, one key per module. R2 also holds the backups
introduced in ADR 0003, and charges no egress, so restores are free.

## Consequences

- A from-scratch build now has a documented apply order: `shared/`, then
  `control-plane/`, then `workers/`.
- Changing the control plane requires deliberately removing `prevent_destroy`
  and clearing the Hetzner protections. That friction is the point, and it
  must be written into the control-plane runbook or it will read as a bug.
- Resources are matched across modules by name. Renaming a shared resource
  silently breaks dependent modules at plan time. Names are now interface.
- Three state files to manage rather than one.
- Terraform state is no longer in git, so `git restore` can no longer touch
  it — and equally, the state's history is no longer in git. R2 object
  versioning replaces that.
