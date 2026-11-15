---
title: "Base layer: app repositories integrate without a project folder"
description: "Why cloud-infra should become a base layer that outside application repositories plug into, what the per-project layout costs today, and the work that builds the contract decided for it."
type: plan
status: active
topics: [gitops, kubernetes, provisioning, security]
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

The contract was decided on 2026-11-15 and is recorded in
[ADR 0015](../adr/0015-applications-are-admitted-through-a-private-registry-and-described-by-a-spec-the-platform-renders.md):
a private Application Registry admits each Application, an Application Spec
describes it, a platform Helm chart renders the inside of its namespace, and
kluster reconciles what lies outside. The work below builds that contract. It
touches the same seams as the provider Plan
([Providers: the platform adapts to more than one](./providers-the-platform-adapts-to-more-than-one.md)),
so the two are designed with each other in view.

## Design

- [x] Decide in a grill where an application's manifests live: in its own
      repository, here, or either, and how Argo CD finds them. Own
      Applications in their own public repository, managed Clients in a
      private repository, both admitted through a private Registry (ADR 0015).
- [x] Decide in a grill the integration contract: what an owner declares
      (hosts, database, Secrets, size) and what the base layer creates and
      enforces from it (namespace, AppProject, LimitRange, route admission,
      probe). Application Spec v1 with Extras, chart inside, kluster outside
      (ADR 0015).
- [x] Decide in a grill how a tenant's Secrets and database are provisioned
      without the tenant's values or ciphertext entering this repository. A
      Secret Catalog per Registry entry in the private repository; an own
      database and role per Application (ADR 0015).
- [x] Decide the migration order for authos, doma, imaps and wasteio.
      tosak-net, doma, then authos; imaps and wasteio do not migrate
      (ADR 0015).

## The chart and the Registry

**Done when:** an Application listed in the Registry with a v1 Spec gets every
object in its namespace from the platform chart through the Registry
generator, and a new image version reaches it through Image Updater, proven in
the rehearsal project.

- [ ] Write the platform Helm chart and its `values.schema.json` for Spec v1:
      components, hosts and `services.database`, with the platform's
      standards (security context, `/tmp`, probe conventions,
      `revisionHistoryLimit`) applied, not taken from the Spec.
- [ ] Create the private Registry repository and the Registry generator: one
      multi-source Application per entry, with the chart from this repository
      and the Spec and Extras from the entry's repository. Argo CD reads the
      private repositories through a GitHub App repository credential.
- [ ] Install Argo CD Image Updater and prove that its Git write-back works
      into a values file of a multi-source Helm Application.

## The outside work in kluster

**Done when:** kluster, in Plan Mode, reconciles each Registry entry's
database and role, Gateway admission, probe target and Secrets from that
entry's own Catalog, and `kluster services check` reports a Registry entry
that misses one of them.

- [ ] Write the reconcile functions: the database with its own role and a
      connection Secret, the namespace in the Gateway's `allowedRoutes`, and
      the probe target per host.
- [ ] Teach `kluster secrets` to read one Secret Catalog per Registry entry,
      in the repository the Registry names.
- [ ] Add an alert on `ImagePullBackOff` and on an expired pull credential.

## Isolation before a Client's code runs

**Done when:** an Application namespace runs under Pod Security `restricted`
with a ResourceQuota and a default-deny NetworkPolicy that the CNI enforces,
proven by a refused connection from a test pod in the rehearsal project and on
the live cluster.

- [ ] Choose the NetworkPolicy enforcer in a grill: Cilium, Canal or
      kube-router.
- [ ] Roll the enforcer out, rehearsal project first, with a default deny per
      Application namespace and the allows the platform services need.
- [ ] Apply Pod Security `restricted` and a ResourceQuota to every
      Application namespace.

## Migration: the pilot ladder

**Done when:** tosak-net, doma and authos run from Specs through the Registry
and deploy through Image Updater, and the `projects` generator is gone.

- [ ] Move tosak-net: Spec, Registry entry, Image Updater; remove its overlay,
      generator path and tag-bump entry.
- [ ] Move doma: `services.database` with its own role, the owner change done
      with a tested R2 restore ready.
- [ ] Move authos: the keystore as an Extra, its own database role and its own
      Redis password; then remove the `projects` generator.

## The first Client

**Done when:** the first Client's Application runs from a managed Spec in the
private repository, admitted through the Registry, after the isolation group
is done.

- [ ] Admit the first Client: Registry entry, Spec, Secret Catalog and image
      pull credential.

## Self-service

**Done when:** a Client with write access to its own Spec can change only
what the contract allows.

- [ ] Enforce the limits self-service needs: allowed kinds for Extras,
      quotas, no `LoadBalancer` or `NodePort`. Trigger: before the first
      Client gets write access.

## Open questions

- How the platform chart is versioned: every Application follows the chart's
  latest revision, or pins a release and moves when its owner chooses.
- What `services.identity` and `services.mail` need first: an Authos client
  made by a reconciler, and a decision on how applications send mail
  ([ADR 0012](../adr/0012-mail-is-self-hosted-on-stalwart-and-sent-through-amazon-ses.md)
  defers it).
- The name of what the base layer is; "cloud platform" may not fit a layer that
  runs on other providers' compute.
