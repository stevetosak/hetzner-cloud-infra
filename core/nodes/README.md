# nodes

The helpers that keep the nodes and their mesh in order (ADR 0016), in the
namespace `nodes`. Synced by the Application `core-nodes`; not in a rehearsal,
so a rehearsal never writes to the public branch `mesh-catalog`.

## The mesh catalog

The mesh store, the ConfigMap `kube-system/mesh-peers`, has no history. The
catalog is its public, versioned record: `peers.json` on the branch
`mesh-catalog`, one commit per change, each machine's name, site, mesh address
and public key. It never holds an endpoint.

```
CronJob mesh-catalog (hourly, minute 17)          mesh-catalog/send.sh
  reads kube-system/mesh-peers
  reads peers.json from raw.githubusercontent.com (no token; 404 = empty)
  same content → exit 0, nothing sent
  different    → POST /dispatches, event mesh-peers, the whole store
    → .github/workflows/mesh-catalog.yml
        mesh-catalog/catalog.mjs from master checks the payload
        and writes peers.json; a commit only when it changed
```

- **A lost dispatch** leaves the difference in place, so the next run sends
  again. A run is safe to repeat, because it sends the whole store.
- **A field other than** `site`, `address` and `publicKey` in the store fails
  the Job (`KubeJobFailed`), and the workflow refuses it as well.
- **The token** is the Secret `mesh-catalog-github` (`secrets.yaml`), a
  fine-grained PAT that may send the dispatch. It is mounted optional: a run
  with nothing to send needs none.
- **Polling records states, not every change**: a store changed and put back
  between two runs leaves no trace.
- **The branch** has a ruleset that forbids force pushes and deletion.

Run it by hand:

```bash
kubectl create job --from=cronjob/mesh-catalog -n nodes mesh-catalog-manual-$(date -u +%Y%m%d%H%M)
```

Tests: `node --test 'core/nodes/mesh-catalog/*.test.mjs'` (needs bash and jq).
