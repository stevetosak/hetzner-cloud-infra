---
title: "Contract validator: onboarding proves an app fits the platform"
description: "Why the platform needs its own service that checks a Client's application against the contract before it deploys, why it waits for self-service, and the design questions that come first."
type: plan
status: proposed
topics: [gitops, kubernetes, security]
---

An application joins the platform through an Application Spec
([ADR 0015](../adr/0015-applications-are-admitted-through-a-private-registry-and-described-by-a-spec-the-platform-renders.md)),
and the platform holds it to a few rules: the Spec's shape, the rules of the
Pod the chart renders from it (non-root, no privilege escalation, a read-only
root filesystem), an image the platform can pull, and a liveness path that
answers when the image runs under the platform's security context. A developer
should learn that an application breaks one of these before it deploys, in
words that name the rule, not from a failed sync in a tool the developer does
not see.

The operator decided on 2026-11-15 that this is a service of its own on
tosak.net: an application that validates a Client's application as part of
onboarding. It is not a script each application copies; every application
gets the same checks from one place. The Base layer Plan keeps the parts the
cluster needs anyway, the chart's schema and the admission rules
([Base layer: app repositories integrate without a project folder](./base-layer-app-repositories-integrate-without-a-project-folder.md)).
The validator waits until Clients host their own applications (self-service);
while hosting is managed, the operator onboards each Client by hand. It is
designed in a session of its own.

## Design

**Done when:** a Decision page records what the validator checks, where it
runs, how a developer uses it, and how it shares its rules with the cluster's
admission.

- [ ] Decide in a grill which checks the validator runs: the Spec's schema,
      the rendered objects, the image, and a smoke run of the image under the
      platform's security context.
- [ ] Decide in a grill how a developer reaches it: a web onboarding flow, an
      API, a CI step in the application's repository, or more than one.
- [ ] Decide in a grill how the validator and the cluster's admission rules
      share one source, so the two never disagree.
- [ ] Decide in a grill where the smoke run executes, and what isolates an
      unknown image while it runs.

## Open questions

- Whether passing the validator is a condition of admission to the
  Application Registry, or only advice to the developer.
- Whether the validator also runs on every new image version after
  onboarding, before Image Updater rolls it out.
