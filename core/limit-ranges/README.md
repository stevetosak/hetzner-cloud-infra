# LimitRanges

One LimitRange `container-defaults` in every application namespace
([Capacity Plan](../../docs/plans/capacity-right-size-then-grow-by-cost.md),
Phase 1). Synced by the Application `core-limit-ranges`.

A container that carries no resources gets a request of `10m` CPU and
`64Mi` memory and a memory limit of `128Mi`: the sizing rule's shape (limit
twice the request), and no CPU limit. A LimitRange acts only at pod admission,
so a container with its own values keeps them and a running pod is not
changed. Every container in `authos` and `doma` sets both requests and
limits today (proven by `kubectl get pods -o json` on 2026-11-03), so the
first sync changes no pod.

- **Layout.** `base/` holds the object without a namespace; each
  `<namespace>/` directory sets its namespace on the base. A new project adds
  its directory and lists it in `kustomization.yaml`.
- **Checked.** `kluster services check` (CI `services-check.yml`) fails when
  a project in the `projects` generator list has no directory here, when the
  directory sets another namespace, or when the `core` AppProject does not
  admit the namespace.
- **Not in a rehearsal.** No application namespace exists there, so the
  rehearsal list leaves `core-limit-ranges` out.
- 🔴 **A memory request over `128Mi` without a limit is refused** at admission,
  because the default limit would be under the request. Set both.
- **Check it:** `kubectl -n <namespace> describe limitrange container-defaults`.
