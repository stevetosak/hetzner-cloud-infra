# 5. Hetzner Volumes, no Longhorn, stable worker names

Date: 2026-09-20

## Status

Accepted. Supersedes the worker-naming decision in ADR 0004; the rest of
ADR 0004 stands.

## Context

The incident's damage ran along a chain, and every link was load-bearing:

1. Longhorn provisions from **worker local disk**.
2. Longhorn binds a node name to a disk UUID, so a recreated worker reusing
   its old name breaks its disks (documented in README).
3. Therefore worker names had to be unique per creation, which introduced
   `node_suffix`.
4. `node_suffix` was **global**, so `cmd/node_add.go` minting a fresh one
   renamed every worker, and a name change forces server replacement.
5. PostgreSQL ran on Longhorn, on those same workers, with a replica on each
   of three nodes.

So a single `node add` destroyed every replica of every volume simultaneously,
and four databases with them.

Workers are deliberately ephemeral. Longhorn made the *worker set* the
durability boundary for data. Those two positions are incompatible, and had
been since before this incident: read literally, every full worker reset ever
run destroyed every database.

An audit found only three manifests using Longhorn at all —
`core/cnpg/pg-cluster.yaml`, and two under `projects/wasteio/`, which ADR 0003
places out of scope. Prometheus declares no storage class and takes the
cluster default. So PostgreSQL was Longhorn's only in-scope consumer.

Hetzner Cloud Volumes are independent resources: deleting a server detaches
them rather than deleting them. Had PostgreSQL been on one, every server could
have been destroyed with no data loss.

## Decision

**PostgreSQL moves to Hetzner Cloud Volumes.** Install `hcloud-csi-driver`
alongside the CCM already required by ADR 0001, and make `hcloud-volumes` the
default StorageClass. Three 10 GB volumes, roughly €1.32/month.

**Longhorn is removed entirely.** With PostgreSQL moved, it has no in-scope
consumer. This deletes the longhorn-system install, the Longhorn ingress and
its `basic-auth` secret, `LonghornStage` from the worker bootstrap pipeline
(open-iscsi, NFS kernel modules, multipathd masking), and `LonghornPreflight`.

**Prometheus takes an `emptyDir`.** Its retention is already 3 days. History is
lost on restart, which is proportionate.

**Worker names become stable** — `k8swk1`, `k8swk2`, `k8swk3`, forever. The
disk-UUID constraint was the only reason they ever needed to change, and it is
gone. `var.node_suffix`, the per-worker `name_suffix` proposed in ADR 0004,
`nodeSuffixNow()`, and `liveNamesByTfvarsKey` with its prefix inference are all
deleted.

A name that never changes cannot force a replacement. The bug class that
destroyed this cluster stops being expressible rather than being guarded
against.

A gate requiring a verified backup before a destructive reset was considered
and rejected: once the data no longer lives on the workers, there is nothing
for it to protect.

## Consequences

- Roughly €1.32/month for storage that previously appeared free — it was not
  free, it was uninsured.
- No ReadWriteMany volumes. Nothing in scope needs one; a future workload that
  does would need Longhorn back, or an alternative.
- Reviving wasteio means rewriting its two `storageClassName: longhorn`
  references first.
- Hetzner Volumes are location-bound to `hel1` and attach to one server at a
  time. Rescheduling a PostgreSQL pod requires a detach and reattach, which is
  slower than Longhorn's local replicas.
- Prometheus loses its history on every restart.
- Because worker names are now reused, a recreated worker must have its old
  Kubernetes Node object deleted first, or it inherits stale taints and labels.
  `reset` already does this; `node remove` must too.
- Terraform must **not** use `create_before_destroy` on workers: Hetzner
  requires server names to be unique among live servers, so the old server must
  be gone before the new one is created.
- `core/monitoring/lean-monitoring.yaml` pins Prometheus and Grafana to
  `kubernetes.io/hostname: k8swk2-20260105-182701`, a dead worker. Stable names
  make such a pin durable for the first time; the value must be corrected to
  `k8swk2`.
