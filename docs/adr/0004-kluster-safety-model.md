# 4. kluster is an assistant, not the backbone

Date: 2026-09-20

## Status

Accepted. The worker-naming decision below is **superseded by ADR 0005**, which
removes Longhorn and with it the disk-UUID constraint that required changing
names at all. Worker names are now stable and the suffix machinery is deleted.
Everything else here — plan mode, intent assertion, the `--apply` abort, and
closing `allow_public_ssh` — stands.

## Context

kluster replaced a set of bash bootstrap scripts. Its first real use destroyed
every worker in the cluster.

Three defects combined:

- **Worker names carried one global `node_suffix`.** `main.tf` set
  `name = "${each.key}-${var.node_suffix}"`, and `cmd/node_add.go` minted a
  fresh timestamp per invocation. Adding one worker therefore renamed all of
  them, and an `hcloud_server` name change forces replacement. The bash script
  this was ported from passed a fresh suffix too, but only ever ran as a
  full reset, where renaming everything was the intent.
- **`Client.Apply` never inspected its plan.** It ran `terraform apply` through
  `tfexec` with no plan step, so "create one server" and "destroy four, create
  five" were indistinguishable — both were a successful apply.
- **`allow_public_ssh=true` was passed on every apply and never revoked**,
  leaving the firewall open to the operator's public IP indefinitely. The
  README describes this firewall as permitting node access only through the
  VPN, which stopped being true after the first `node add`.

Note also that the incident's most expensive loss — the control plane — is
addressed structurally in ADR 0002, not here.

## Decision

**kluster is a convenience layer over Terraform and the runbook, never the
authority.** Terraform state and the written procedure are the backbone. kluster
must default to showing rather than doing, and must never be the only thing
that knows how the cluster is built.

**Per-worker name suffixes.** *(Superseded by ADR 0005 — worker names are now
stable and carry no suffix. Retained here because the reasoning explains why
the original global suffix was wrong.)* Each entry in the workers map carries its own
`name_suffix`, written once at creation and never rewritten.
`name = "${each.key}-${each.value.name_suffix}"`, and `var.node_suffix` is
deleted. `reset` rewrites every suffix; `node add` writes only the new entry's.

This is the only scheme that expresses recreating a *single* worker with a
fresh name, which a global suffix cannot — and which the Longhorn disk-UUID
constraint in the README requires. It also makes a worker's live name exactly
derivable from tfvars, so `liveNamesByTfvarsKey` and its prefix inference can
be deleted from `node_list`, `node_remove` and `node_add`.

**Plan mode is the default.** Every mutating command computes a plan, prints
it, and changes nothing. Acting requires an explicit `--apply`. The existing
`--dry-run` flag is kept as an accepted no-op.

**Intent assertion.** Each command declares what it expects the plan to
contain — `node add`: 1 create, 0 destroys; `node remove`: 1 destroy, 0
creates; `reset`: N destroys and N creates over a named set — and kluster
checks the plan JSON against it.

On mismatch: **warn in plan mode, abort under `--apply`**, with a non-zero exit
and a printed expected-versus-planned diff. `--allow-unexpected` overrides, for
when the mismatch is legitimate. Plan mode warns rather than aborting because
its whole job is to show you what is about to happen.

**`allow_public_ssh` is closed again.** Any command that opens it applies the
default back once bootstrapping finishes.

## Consequences

- Every mutating command costs a plan-then-apply round trip.
- Intent assertions are a second description of what each command does, and
  will drift from the implementation unless tested. They need tests that fail
  when the two disagree.
- `--allow-unexpected` is an escape hatch, and escape hatches get reached for
  under pressure. It should print what it is overriding.
- Inverting the default changes muscle memory: commands that used to act now
  only report. That is intended.
- Existing worker entries in `terraform.tfvars` must gain a `name_suffix`
  matching their current live names, or the first apply renames and therefore
  destroys them. This migration is the exact failure the ADR exists to prevent
  and must be done by reading live names, not by guessing.
