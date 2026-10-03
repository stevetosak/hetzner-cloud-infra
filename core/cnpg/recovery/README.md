# core/cnpg/recovery

JSON patches that turn `../pg-cluster.yaml` into a recovery manifest. They are
applied with `kubectl patch --local`, which needs no cluster and no extra tool.
The procedure that uses them is
[docs/runbook/pg-recovery.md](../../../docs/runbook/pg-recovery.md).

| File | Turns | Into |
|---|---|---|
| `recover.json` | `pg-cluster.yaml` (generation `g1`) | a `bootstrap.recovery` Cluster that reads `g1` and archives to `g2` |
| `rehearsal.json` | the output of `recover.json` | the same Cluster as `rehearsal` in `pg-rehearsal`, archiving to `tosak-pg-cluster-rehearsal` |
| `rehearsal-objectstore.json` | `../objectstore.yaml` | its copy in `pg-rehearsal`, without `retentionPolicy` |

Each patch starts with a `test` op. `recover.json` refuses to run unless
`pg-cluster.yaml` archives to `tosak-pg-cluster-g1`, so a patch left behind
after a recovery fails loudly instead of reading the wrong generation.
`rehearsal.json` refuses anything but the `g2` output, so a rehearsal can never
archive into the real next generation.

🔴 **After a real recovery, bump all three generations here** (`g1` → `g2` in
the tests, the source and the `externalClusters` entry; `g2` → `g3` in the
replace and in `rehearsal.json`'s test) in the same commit that changes
`serverName` in `pg-cluster.yaml`.
