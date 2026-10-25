# core/cnpg/cluster

The database Cluster `tosak-pg-cluster`, outside ArgoCD on purpose.
`kluster db create` builds and applies it (ADR 0009, Amendment 2026-10-24;
the procedure is [docs/runbook/pg-recovery.md](../../../docs/runbook/pg-recovery.md)).
The two renders are sibling directories because kustomize refuses a base
that contains its own overlay.

| Directory | Built by | What it is |
|---|---|---|
| `fresh/` | `db create --fresh` | `pg-cluster.yaml`: an empty `initdb` Cluster that archives to the current Archive Generation |
| `recover/` | `db create --recover` | the same Cluster with `bootstrap.recovery`: it reads the current generation through the source store `tosak-pg-backups-source` and archives to the next |

`recover.json` starts with `test` ops: the build fails unless
`fresh/pg-cluster.yaml` still names the generation the overlay reads, so a patch
left behind after a recovery fails loudly instead of reading the wrong
generation. `kluster services check` refuses an overlay whose test is not
the base's `serverName` or whose replace is not the next generation.

After a live recovery passes, `kluster db create` writes the bump itself
(`serverName` here, the tests, the source and the replace in `recover.json`)
and refuses the next run until it is committed. A rehearsal never writes
these files: its Cluster is this one, with only the archive store pointed at
the rehearsal bucket (`core/rehearsal/cnpg`).
