---
title: "Base layer: app repositories integrate without a project folder"
description: "Why cloud-infra should become a base layer that outside application repositories plug into, what the per-project layout costs today, and the design questions that come before any change."
type: plan
status: proposed
topics: [gitops, kubernetes, provisioning]
---

cloud-infra holds every application's manifests in its own `projects/<name>/`
folder. That fits one tenant, the operator, who owns every application. It
does not fit the direction the operator set on 2026-11-08: an open-source base
layer that other application repositories integrate with, where each owner
decides whether its application is open or closed source. The repository stays
public as that base layer's record.

Adding a project today touches the base layer itself, read from the
repository's `CLAUDE.md` and the files it names on 2026-11-08: the project's
folder, a path in the `projects` ApplicationSet generator
(`core/argocd/applicationset.yaml`), an AppProject in
`core/argocd/appprojects.yaml`, a LimitRange directory under
`core/limit-ranges/`, an entry in `deployments/apps.json`, the namespace in the
Gateway's `allowedRoutes`, a target in the public-hosts Probe, its Secrets in
`secrets.yaml`, and a database file under `core/cnpg/databases/`. Every step
needs write access here, so an outside owner cannot integrate alone, and a
closed-source owner's manifests would land in a public repository.

The work waits until the operator's own work is done and the cluster is
stable, and it is taken up when the operator starts it. It touches the same
seams as the provider Plan
([Providers: the platform adapts to more than one](./providers-the-platform-adapts-to-more-than-one.md)),
so the two are designed with each other in view.

## Design

**Done when:** a Decision page records how an application repository
integrates with the base layer, what the base layer offers it and enforces on
it, and how an application that already lives under `projects/` moves to that
model.

- [ ] Decide in a grill where an application's manifests live: in its own
      repository, here, or either, and how Argo CD finds them.
- [ ] Decide in a grill the integration contract: what an owner declares
      (hosts, database, Secrets, size) and what the base layer creates and
      enforces from it (namespace, AppProject, LimitRange, route admission,
      probe).
- [ ] Decide in a grill how a tenant's Secrets and database are provisioned
      without the tenant's values or ciphertext entering this repository.
- [ ] Decide the migration order for authos, doma, imaps and wasteio.

## Open questions

- Who is a tenant: only the operator's own projects in separate repositories,
  or outside owners with their own access? The answer sets how much isolation
  the base layer must enforce.
- Whether the deploy pipeline (tag-bump PRs and `deploy-scope` on this
  repository) stays, or each application deploys from its own repository.
- The name of what the base layer is; "cloud platform" may not fit a layer that
  runs on other providers' compute.
