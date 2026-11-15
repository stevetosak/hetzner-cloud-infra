---
title: "How the platform grows: every service compounds"
description: "The rule the tosak platform is built by: each service is a declaration, a reconciler and a check, made once and reused; where the cluster already follows it, where it does not yet, and when a pattern is worth extracting."
type: explanation
topics: [gitops, provisioning, kubernetes]
---

The tosak platform is run by one operator and is meant to host many
applications, tosak's own and its Clients', on more than one provider's
compute. Those two facts do not fit together if every new application, domain
or service costs the same manual work as the last one. The rule the platform
is built by answers that: **everything built here is made for reuse, with a
standard and automated configuration, so that the second use of a service is
trivial and a pattern proven in one service becomes available to the whole
cluster.** The operator set it down on 2026-11-15, after mail was working, in
the words "everything we make is compoundable and usable". Work compounds when
each piece makes the next one cheaper; it only adds up when each piece costs
the same again.

## A service that compounds has three parts

A service compounds when it has a declaration, a reconciler and a check.
The declaration is a short file that says what is wanted, in the language of
the need and not of the mechanism: a Secret and where its keys come from, a
host and the status it should answer with, an application and the database it
needs. The reconciler reads the declaration and makes the world match it,
safely and more than once: a second run that finds nothing to do does nothing.
The check compares the world, or the repository, with the declaration and
fails loudly on any gap, so a missing piece is found by a machine and not by a
reader months later.

Each part covers a weakness of the others. A declaration without a reconciler
is documentation that drifts. A reconciler without a declaration is a script
that encodes one case and has to be edited for the next. Neither is trusted
without a check, because a change that skipped a step looks the same as one
that did not until something breaks. Together the three turn adding a case
into writing one more entry, and the cost of the next case falls towards the
cost of that entry.

The Secret Catalog is the clearest case in this repository.
[`secrets.yaml`](../../secrets.yaml) declares every Secret and the source of
each key; `kluster secrets generate`, `pull` and `apply` reconcile the
encrypted files and the cluster with it; and `kluster secrets check` refuses a
file that is not listed or not fully encrypted, both in the pre-commit hook and
in CI ([`secrets-check.yml`](../../.github/workflows/secrets-check.yml)). A new
Secret is one entry, never a procedure. The public-hosts probe follows the
same shape on a smaller scale: each host is a target in one Probe, and
`kluster services check` fails on a host that has no target.

## Where the cluster does not compound yet

Mail is the example that set the rule down. The mail server works and every
message leaves through Amazon SES
([ADR 0012](../adr/0012-mail-is-self-hosted-on-stalwart-and-sent-through-amazon-ses.md)),
but adding a domain is still a list of steps in a Procedure: an SES identity
with Easy DKIM and a MAIL FROM domain, the domain in Stalwart, its accounts,
and seven DNS records the domain's owner publishes
([Email Plan, Phase 4](../plans/email-inboxes-on-stalwart-sending-through-ses.md#phase-4--client-domains)).
Of the three parts, only the check exists:
[`dns_check.py`](../../projects/mail/dns_check.py) proves any domain's records
against the list. The compounding version declares a mail domain in one entry
and lets a reconciler make the SES identity and the Stalwart domain from it,
with the check as the finish line.

Applications were the larger case. Adding one touched about nine places in
this repository, and every application carried its own copy of the platform's
standards. `kluster project add` writes those places, which saved typing but
not the shape: the standards were still copied, and a Client's application
would still have landed in a public repository.
[ADR 0015](../adr/0015-applications-are-admitted-through-a-private-registry-and-described-by-a-spec-the-platform-renders.md)
gives applications the three parts: the Application Spec is the declaration,
the platform's Helm chart and kluster's reconcile functions are the
reconciler, and the chart's schema and `kluster services check` are the
check. Its `services` block is where the platform's own services meet the
applications: a database first, and identity and mail each when their
reconciler exists. Every service the platform offers becomes one more member
of that block, and every application that already has a Spec can use it by
adding one line.

## When a pattern is worth extracting

Not everything compounds, and not everything should. A pattern is extracted
for the whole cluster when it has merit there, and the usual sign is the
second use: the second application that needs the same object, the second
service that repeats the same manual step, the second domain added by the same
procedure. The Extra in ADR 0015 encodes this. An object an application
carries beside its Spec is accepted as it is, and the moment two applications
carry the same kind, the Spec learns it. Building the general form before the
second use designs for a need that has not shown its shape.

Some parts stay specific on purpose. Stalwart has its own load balancer, four
ports and an init container, and it is a platform service rather than an
application, so it does not go through the Spec at all. The same test applies
in the [Providers Plan](../plans/providers-the-platform-adapts-to-more-than-one.md),
where each part bound to Hetzner either moves behind an interface or stays
provider-specific, depending on whether a second provider will really use it.

The rule also shapes how work is written down. A choice that makes a pattern
reusable is recorded where the next service will find it: a Decision page
when it is hard to reverse, a term in the [glossary](../../CONTEXT.md) when it
names something new, and a check when it can be enforced. The platform stays
open source for the same reason it compounds: the record of how it is built is
part of what the next user relies on.
