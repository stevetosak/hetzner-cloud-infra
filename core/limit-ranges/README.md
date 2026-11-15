# LimitRanges

One LimitRange `container-defaults` in every application namespace
([Capacity Plan](../../docs/plans/capacity-right-size-then-grow-by-cost.md),
Phase 1). Synced by the Application `core-limit-ranges`.

A container that carries no resources gets a request of `10m` CPU and
`64Mi` memory and a memory limit of `128Mi`: the sizing rule's shape (limit
twice the request), and no CPU limit. A LimitRange acts only at pod admission,
so a container with its own values keeps them and a running pod is not
changed.

The platform's own namespaces (`authos`, `doma`, `mail`, `tosak-net`) add the
component [`own-workloads`](own-workloads/kustomization.yaml), which removes
the default memory limit. Their containers carry no memory limit, because the
Vertical Pod Autoscaler sets their requests in place
([Autoscaling Plan](../../docs/plans/autoscaling-workloads-grow-without-a-manual-resize.md));
a default limit would come back at admission. A client namespace keeps the
base with its default limit.

- **Layout.** `base/` holds the object without a namespace; each
  `<namespace>/` directory sets its namespace on the base, and an own
  namespace adds `own-workloads`. A new project adds its directory and lists
  it in `kustomization.yaml`.
- **Checked.** `kluster services check` (CI `services-check.yml`) fails when
  a project in the `projects` generator list has no directory here, when the
  directory sets another namespace, or when the `core` AppProject does not
  admit the namespace.
- **Not in a rehearsal.** No application namespace exists there, so the
  rehearsal list leaves `core-limit-ranges` out.
- 🔴 **In a namespace with the default limit, a memory request over `128Mi`
  without a limit is refused** at admission, because the default limit would
  be under the request. Set both there.
- **Check it:** `kubectl -n <namespace> describe limitrange container-defaults`.
