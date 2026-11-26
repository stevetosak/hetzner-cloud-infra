# nodes

The helpers that keep the nodes and their mesh in order (ADR 0016), in the
namespace `nodes`. Synced by the Application `core-nodes`. A rehearsal syncs
its cut, `core/rehearsal/nodes`: node-controller without the CronJob
mesh-catalog, so a rehearsal never writes to the public branch `mesh-catalog`.

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

## node-controller

Removes a dead Worker on any provider (ADR 0016, Amendment 2026-11-24): after
20 minutes not Ready it gets the `node.kubernetes.io/out-of-service` taint, so
its pods are force-deleted and start elsewhere, and once they are gone (or 5
minutes later) its Node is deleted. It never touches the Control Plane.

```
every 30 s: Nodes and VolumeAttachments from the cache
  internal/deadnode.Decide → taint | delete | untaint | hold
    Worker Ready again while tainted         → untaint
    not Ready < 20 min                       → nothing
    pause-until not passed (or unreadable)   → hold  (paused | pause-invalid)
    half or more of the Workers not Ready    → hold  (mass-guard)
    a VolumeAttachment, provider not "off"   → hold  (volume-attached)
    otherwise                                → taint, then delete
```

- **Pause it for planned work** on one Node, until a UTC time:
  `kubectl annotate node <name> tosak.net/dead-node-remover.pause-until=2026-11-25T18:00:00Z`.
  The pause ends by itself; `kubectl annotate node <name> tosak.net/dead-node-remover.pause-until-` ends it now.
- **Bring a removed machine back** with `kluster node add <name>`: a machine
  that kept running never registers its Node again, and one that booted again
  comes back without its profile labels.
- **What it tells**: `NodeRemoved` (a notice), `NodeRemovalHeld` and
  `NodeControllerDown` (warnings) in the Alert Chat
  (`core/monitoring/rules/node-controller.yaml`), and Events on the Node:
  `kubectl get events -n default --field-selector involvedObject.kind=Node`.
- **The image** is `ghcr.io/stevetosak/kluster-node-controller`, built and
  pushed by `.github/workflows/node-controller.yml` on a merge to `master`
  that changes `tools/kluster`. A new image goes live only through a pull
  request that pins its digest in `kustomization.yaml`, never through
  tag-bump: this controller can delete Nodes.
- **Tests**: `go test ./internal/deadnode ./internal/nodecontroller` in
  `tools/kluster`, and the k3d test against a real API server,
  `tools/kluster/internal/nodecontroller/k3d-test.sh`.
