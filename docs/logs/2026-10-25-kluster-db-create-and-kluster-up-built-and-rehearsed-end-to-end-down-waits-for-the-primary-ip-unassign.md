---
title: "kluster db create and kluster up built and rehearsed end to end, down waits for the Primary IP unassign"
description: "What the db create and kluster up tasks built (2026-10-24 and 2026-10-25), what each rehearsal proved and found, and what stays open."
type: log
date: 2026-10-25
topics: [backups, databases, provisioning]
---

Two tasks of Phase 6 in the [rebuild Plan](../runbook/rebuild-2026-09-20.md) close here. The first, `kluster db create --fresh|--recover` (pull request 57, merged), gave the database Cluster a command of its own. The second, `kluster up --db fresh|recover`, composes every single command into one run that fills an empty project, and was proven end to end in the rehearsal project. The design of each is recorded as an Amendment of [ADR 0009](../adr/0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md#amendments), dated 2026-10-24 and 2026-10-25; the operator chose every answer in two reviews of eleven questions each.

## db create

`db create` applies one of two renders of the Cluster into an empty slot and proves what `ContinuousArchiving=True` does not: objects in the bucket. Its Stages, each with the section of [the recovery runbook](../runbook/pg-recovery.md) it carries out, are `db-preflight`, `db-cluster`, `db-healthy`, `db-archive`, `db-marker`, `db-replication`, `db-switchover` (a rehearsal only), `db-first-backup` and `db-generation` (a live recovery only). The renders are sibling kustomize directories, `core/cnpg/cluster/fresh/` and `core/cnpg/cluster/recover/`; the first layout put the recovery inside the fresh one, and kustomize refuses a base that contains its own overlay. The overlay's JSON patch starts with `test` operations, and a unit test proved that kustomize's library runs them: a base on `g2` fails the recover build.

The rehearsal archives to its own bucket. R2 tokens scope to buckets and not to prefixes, so the `rehearsal/` prefix of the earlier Amendment could not keep a rehearsal token away from the live bucket. A read-only Source Store, `tosak-pg-backups-source`, is the one place any recovery reads from, in both environments. The Secret Catalog gained `rehearsalItem:`, so a rehearsal never holds a live value.

The rehearsal of 2026-10-24 passed `--fresh` at every Stage (switchover to instance 2, timeline 2 archived, no recovery marker, first base backup with `backup.info` and `data.tar.gz`), resumed on a second run, refused `--recover` while the Cluster existed, and passed `--recover` after the Cluster was deleted by hand. The restored `doma` database has 33 tables, as live does. It found three faults, all fixed in the task:

- A parser read 24 hex digits as a 64-bit number, so no WAL segment name ever parsed. Unit tests against a fake bucket showed it.
- `db-archive` demanded that the new timeline open after the source's newest segment. It opens inside that segment when the last record runs past it, so the test is now "at or after".
- A `Backup` object has no owner, so a deleted Cluster leaves its first backup behind, and the recovery would have counted it as its own. Only Backups created after the current Cluster count now.

The same rehearsal showed that a `kubeadm init` cut off by a kill left manifests and bound ports behind, and the rerun failed its dry run. `cp init` now resets an unfinished build of its own (marker `running`) first. A SIGINT during the Flannel step resumed cleanly on the rerun. That settles the kubeadm half of the Plan item on a half-failed `kubeadm init`.

## kluster up

`up` fills the empty slots in order: shared, Control Plane, every Worker the Worker set declares, the seed, the database, the zone check. It changes nothing that exists, and it never writes the Worker set. Before anything opens, `up-preflight` runs every check that needs no cluster and prints all failures together: the working tree is the revision the environment's ArgoCD reads, with no tracked change in the paths `up` builds from; the Worker set is committed and not empty; no dead Worker exists; the Control Plane slot is empty or kluster's own; every Bitwarden item is readable. One bootstrap SSH gate covers the Control Plane and every Worker and is closed, with a readback, before the seed. Three details differ from the design as chosen:

- A dead Worker without a cluster is a server other than the Control Plane when no Control Plane exists or when it is older than the Control Plane, and a server the Worker set does not declare is refused too.
- The bucket checks sit in the preflight only while the project has no Control Plane. On a rerun the archive generation is no longer empty, so they run inside `db create`.
- A rehearsal's zone Stage cannot compare with the live load balancer address: a rehearsal does not know it, and it is the origin behind Cloudflare, which stays out of the public repository. The Stage passes when each host has exactly one A record, all the same, and none at the rehearsal load balancer.

The seed gained the Stage `rehearsal-cuts`. It reads the running rehearsal and shows that each live link is cut, from a table in `internal/seed/cuts.go`; a test fails when a directory in `core/rehearsal` has no entry. `kluster down` now waits for a Primary IP to be unassigned before it deletes it, and prints one line when it starts to wait. The rehearsal Worker set `infra/workers/rehearsal.tfvars` declares `k8swk1` to `k8swk3` for good.

## What the rehearsal showed

All of it ran on 2026-10-25 in the rehearsal project. Plan Mode from empty showed the shared plan with nine creates and every later step as a new Stage. `up --db fresh --apply` took about 20 minutes (13:26 to 13:44 UTC) and finished every Stage; `rehearsal-cuts` found all six cuts holding, and the first base backup landed under `g1/base/`. A rerun with `--db fresh` built nothing and resumed the database Stages as done. A rerun with `--db recover` took the second case of the database step (the Cluster "was made by kluster db create --fresh, archives to tosak-pg-cluster-g1 and is healthy") and ran no Stage, which proved that case on real hosts.

After `down --apply`, which emptied the project and deleted 14 objects from the rehearsal bucket (read back empty), `up --db recover --apply` rebuilt the cluster from the live backups. The new timeline opened after the live bucket's newest segment, the first backup landed under `g2/base/`, and a data check on the Control Plane (root over SSH with the pinned host key, `psql`, then the port closed and read back) found 33 tables in `doma` as on live and 15 in `authos`, with the Cluster healthy at 3 of 3. The rehearsal wrote no generation bump.

The second `down --apply` hit the race for real. It printed `primary IP … is still assigned: waiting up to 2m0s for Hetzner to unassign it`, and the Primary IP then went with the load balancer. Before the fix this was the `must_be_unassigned` failure of 2026-10-24. The project was empty and the bucket (16 objects) read back empty.

## Findings

- 🔴 The rehearsal kubeconfig on the laptop, `~/.config/kluster/rehearsal/kubeconfig`, points at `https://10.100.0.1:6443`, and the laptop's `wg0` is the live tunnel at that address. A `kubectl` with that file reached the live API server, and TLS refused it (`x509: certificate signed by unknown authority`), so nothing ran. Proof: `ip route get 10.100.0.1` gives `dev wg0`. kluster itself proves the route through its own in-process peer. While `wg0` holds live, look at a rehearsal with `kluster --env rehearsal ssh open --apply` and root on the Control Plane, then `ssh close --apply` ([rehearsal runbook](../runbook/rehearsal.md#look-at-a-running-rehearsal)).
- With the hcloud provider 1.69.0 the Worker firewall kept port 22 after the close apply, on every close in the run. kluster cleared it over the API and read it back, as its readback is built to do.
- A rerun of the seed applies the hook Job `argocd-redis-secret-init` again, because its 60-second TTL deletes it and the Stage sees a difference. It is harmless, and a Plan item covers the Stage.
- The rehearsal zone has one host in scope, `argocd.tosak.net`: the cut removes the `projects` ApplicationSet, so only core routes remain.
- The Control Plane Plan Mode preview of `private-network` shows `macaddress: ""` instead of `<MAC>`. Cosmetic, not fixed.

## Left open

The Plan gains three items: pre-join Stages on all Workers at once (the hub file on the Control Plane would race, so only the join stays serial), a seed that skips TTL-deleted hook Jobs, and the `macaddress` preview. Open from before: the root SSH half of the `kubeadm init` item, SIGPIPE handling, and the database gate, which blocks retiring a dead Worker while a Cluster instance is not ready.
