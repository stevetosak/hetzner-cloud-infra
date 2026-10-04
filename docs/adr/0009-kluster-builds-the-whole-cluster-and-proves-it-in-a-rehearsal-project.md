---
title: "kluster builds the whole cluster, and proves it in a rehearsal project"
description: "Why kluster grows from a Worker helper into the tool that builds the Control Plane and the services too, which limits hold it, and how it is proven before the live cluster relies on it."
type: decision
status: accepted
date: 2026-10-04
topics: [provisioning, kubernetes, networking]
---

[ADR 0004](./0004-kluster-safety-model.md) framed kluster as an assistant over Terraform and the runbook, and
kept it away from the Control Plane. Since then the cluster has been built
twice, both times by hand from the runbooks, and the kluster code on
`feat/kluster-cli` (PR #10, one commit from 2026-09-13) has fallen behind
everything the rebuild changed. It still expects one Terraform root at
`infra/`, mints a suffix for Worker names, runs a Longhorn stage, applies
with `-auto-approve` and no plan, regenerates the hub `wg0.conf` from a
template, reaches the Control Plane on its public address as `root`, and
ignores SSH host keys. It builds and its unit tests pass against fakes, which
says nothing about the cluster as it is now. That was read from the branch in
a scratch copy on 2026-10-04 (`go build`, `go vet`, `go test ./...`).

The question was therefore not how to fix the branch but what kluster is for.
The operator's answer is the whole cluster: Control Plane, Workers and the
cluster services, from an empty Hetzner project to a running cluster, on the
condition that secrets have a reliable path too ([ADR 0003, Amendments](./0003-recoverability.md#amendments)).
That widening is hard to reverse, because it moves how the cluster is built
from pages into code, and it is surprising next to ADR 0004's "kluster never
touches the Control Plane". So the limits that make it safe are recorded here.

## The decision

**kluster builds the whole cluster, inside six limits.** The runbooks stay the
authority, and everything ADR 0004 says about Plan Mode, the Intent Assertion
and the `--apply` abort stands.

**The Control Plane is created only into an empty slot.** `kluster cp init`
runs only when no `k8s-cp` exists. Its Intent Assertion is one create and no
destroy, replace or update in the `control-plane` Module, and only the SSH
rule in `shared`. It never lifts `prevent_destroy`, `delete_protection` or
`rebuild_protection` ([ADR 0002](./0002-control-plane-isolation.md)) and never destroys. A deliberate replacement
stays the manual procedure in `infra/README.md`; `cp init` builds the new
server once the old one is gone.

**The runbook leads and each Stage cites it.** A Stage carries out one runbook
section and names it, in the code and in its output. A change to a Stage's
commands changes the runbook in the same pull request. kluster is still never
the only record of how the cluster is built.

**Plan Mode covers the hosts, and Stages resume.** Each Stage has a read-only
Probe — the readback its runbook section uses as proof — and an Act. Plan
Mode may open SSH to read but never to write: it shows each edit to an
existing host as a diff against what the host holds now (the hub `wg0.conf`,
`/etc/hosts`, the operator's own `wg0.conf`), and for a new host the commands
each Stage would run. Under `--apply` each diff is checked again before it is
written, and a run that stopped halfway resumes at the first Stage whose Probe
says it is not done.

**Bootstrap SSH is closed and the close is read back.** The hcloud provider
1.69.0 reports success when it takes a firewall from one rule to none and
leaves the rule in place; both runbooks met it. kluster closes through
Terraform, reads the firewall back over the Hetzner API with `hcloud-go`,
clears it with `set_rules []` if the rule survived, reads it back again, and
expects a clean `terraform plan`. The close runs at the end of every run,
failed runs included, and the run exits non-zero while port 22 is still open.

**Host keys are known before the first login.** kluster makes an ed25519
host key pair for each new server and passes it in cloud-init `user_data`,
then pins the public half. Both Modules ignore changes to `user_data`, so
adding it never plans a replacement of a live server. The SSH client's
`InsecureIgnoreHostKey` goes.

**The Worker set is written by kluster and committed by the operator.**
`node add` writes the new entry into `infra/workers/terraform.tfvars` with the
next free private and VPN address, and `node remove` deletes it. Under
`--apply` kluster refuses to run Terraform until that file is committed.
Terraform state lives in R2 and is shared by every clone, so an uncommitted
Worker would read as a planned destroy from any other checkout.

Around those limits, Reset becomes rolling — `node replace` one Worker at a
time, the next only once the node is Ready and the database healthy — and
`kluster up --db fresh|recover` composes the single commands with one combined
plan. When `up` follows a `cp init`, the existing Workers belong to the dead
cluster and are replaced. The hub's WireGuard key is made new with each
Control Plane, and kluster rewrites the operator's own `wg0.conf` to match.

**Nothing of this reaches the live cluster before a rehearsal has run it.**
`--env rehearsal` points kluster at a second Hetzner project with its own
token and its own R2 state prefix. Resource names stay the same, because a
Hetzner project is its own namespace, so the Modules do not change. A patch
set over `core/` — the same `kubectl patch --local` model as `pg-rehearsal` —
cuts every link to the live outside world: the Let's Encrypt staging issuer,
no Cloudflare writes, ArgoCD notifications off, CNPG archiving to a
`rehearsal/` prefix with its own token, and `--db recover` reading the live
bucket with a read-only token. Each cut has a Probe, and `up` refuses to go on
while a live link remains. `kluster down` deletes the rehearsal project's
resources and is refused for the live one.

## Why this and not the alternatives

Keeping kluster to Workers would have left the most expensive procedure — the
Control Plane, the one the 2026-09-13 loss could not repeat — as the only one
with no automation, and the services after it as seventeen manual sections.
Retiring kluster was the honest alternative for a small cluster with rare
builds; it lost because a total loss is exactly when a long manual procedure
is most likely to go wrong.

Letting `cp init` also replace a live Control Plane would make a rebuild one
command, and would remove the friction ADR 0002 added on purpose. Rebasing
PR #10 and fixing in place would keep a history and a pull request whose
description teaches the design this page rejects, so the work starts on a
fresh branch and takes over only what is still right: the SSH client and its
tests, the `Stage`/`Runner` seam, and the fan-out that lets one Worker fail
without cancelling the others. PR #10 is closed with a link.

For the hub key, keeping one key in SOPS would let every peer reconnect after
a rebuild without an edit. The operator chose a fresh key and a kluster edit
of the laptop instead, so no hub private key is stored at all. For host keys,
trust on first use with kluster's own `known_hosts` was weighed and lost to
the one case it cannot cover, a first login answered by the wrong machine.

Proving on the live cluster in risk order would have been cheaper, and would
have left `cp init` and `up` — the commands that matter after a loss — unproven
until a loss. Local VMs would not have exercised the provider defects that
already bit twice. Prices read from the Hetzner pricing API on 2026-10-04,
EUR net in `hel1`: four `cx23` servers at 0.0088 an hour each, an `lb11` at
0.0120, a primary IPv4 at 0.0008 and three 10 GB volumes at 0.0572 per GB per
month come to about €0.05 an hour.

## What it costs

A rehearsal run — a fresh build, a recovery and a teardown — costs about
€0.10–0.25. A forgotten rehearsal costs the monthly caps, about €32 a month,
and Hetzner has no spending cap to stop it (assumed: none was found). So
`recoverability-watch.yml` gains one check: the rehearsal project is empty, or
its oldest resource is younger than six hours, or the operator gets a Telegram
alert.

The bigger cost is the size of the work: five chunks in the order the Plan
records, each closed by a rehearsal run. Each runbook section now has a second
description in code, and the rule that the runbook leads holds only as long as
pull requests are reviewed against it. kluster also gains write access to the
operator's laptop for the WireGuard edit, so a second operator device after a
Control Plane rebuild still needs editing by hand.

Worth revisiting if a rehearsal run ever touches the live outside world, if
the cost check fires more than once, or if the runbooks and Stages drift in a
review: the last would mean the Stages should generate the procedure instead.

## Amendments

**2026-10-04 — the rehearsal state gets its own bucket, and the seeded host
key is replaced at the first login.** Two details above changed while the
safety core was built, both chosen by the operator.

The decision said the rehearsal keeps its Terraform state under "its own R2
state prefix". A prefix in the live bucket would have used the live bucket's
token, so a fault in how kluster picks the state key could write rehearsal
state over a live one, and only kluster's own code would stand in the way. The
rehearsal state lives instead in the bucket `hetzner-cloud-infra-rehearsal`,
reached with an R2 token scoped to that bucket alone: a rehearsal run holds no
credential that can write live state. kluster's configuration refuses a
rehearsal environment that names the live bucket, token or keys, and refuses
at run time a rehearsal value equal to the live one
([rehearsal runbook](../runbook/rehearsal.md)).

The decision also said kluster pins the seeded host key. Hetzner serves
`user_data` on the metadata service to every process on the server for its
whole life, pods included unless something blocks them (assumed, not tested),
so the private half of a seeded key stays readable after boot. The seeded key
now verifies the first login and nothing more: during that login kluster
makes a new key on the host, reads its public half over the verified
connection, and pins it alone ([host keys runbook](../runbook/host-keys.md)). The weighed
alternative, keeping the seeded key, would have let anything that reads the
metadata service pose as the host to an SSH client.
