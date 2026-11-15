---
title: "Applications are admitted through a private Registry and described by a Spec the platform renders"
description: "How an application, tosak's own or a Client's, joins the platform without a folder in this repository: what it declares, what the platform makes and enforces from that, and the order in which the applications already here move over."
type: decision
status: accepted
date: 2026-11-15
topics: [gitops, kubernetes, security, secrets, provisioning]
---

Every application on the cluster lives in this repository today, in its own
`projects/<name>/` folder, and adding one touches about nine places: the
folder, a generator path, an AppProject, a LimitRange, the deploy catalog, the
Gateway's allow list, a probe target, the Secret Catalog and a database file
([Base layer Plan](../plans/base-layer-app-repositories-integrate-without-a-project-folder.md)).
That suits one owner who owns every application. It does not suit the
direction the operator set on 2026-11-08 and sharpened on 2026-11-15: a
platform that hosts applications for Clients, offers its own services
(identity through Authos, mail) to any of them, and runs on more than one
provider's compute. A Client decides whether its application is open or closed
source, and this repository is public, so nothing of a Client's application
can live here.

The second force is the way the platform is meant to grow. **Every service the
platform offers is built for reuse, with a standard, automated configuration,
and a pattern that proves itself in one service is extracted for the whole
cluster.** Mail showed the gap: the server works, but adding a domain is still
a list of manual steps. The per-project layout has the same shape: each
application repeats the same steps by hand, and nothing the platform learns
from one application reaches the next.

The third force is that the cluster was built for one trusted owner. Flannel
enforces no NetworkPolicy ([core/cni/README.md](../../core/cni/README.md)), the
role `tosak` owns every database
([ADR 0007](./0007-shared-infrastructure-is-named-for-the-organisation.md)),
and the applications share one Redis password. None of that is safe once code
the operator did not write runs beside the platform. The terms used below,
Client, Application Spec, Extra and Application Registry, are defined in the
[glossary](../../CONTEXT.md).

## The decision

An application joins the platform through the **Application Registry**, a
private list of admitted Applications: each one's name, its owner, and where
its Application Spec lives. The Registry feeds one Argo CD generator and the
outside work kluster does; an Application that is not in it gets nothing.
Admission is a decision tosak makes, so a repository that merely holds a Spec
is not enough. The Registry is private because it names Clients. tosak's own
Applications keep their Spec in their own public repositories; a managed
Client's Spec lives in a private repository the operator owns; a self-service
Client, later, keeps it in its own. Argo CD reads the private repositories
through a GitHub App repository credential.

The **Application Spec** says what an Application needs, never how the cluster
provides it. Version 1 (`apiVersion: tosak.net/v1`) holds the components, each
with an image and a size and optionally a port, health paths, replicas,
environment and Secret keys; the hosts, each mapped to a component and an
optional path; and `services.database`, which gives the Application its own
database, its own role and a Secret with the connection values. A component
without a port is a worker and gets no Service. The writable `/tmp`, the
security context and the probe conventions come from the platform, not from
the Spec. Identity and mail join `services` later, each when its reconciler
exists, so every new platform service is one new member of that block and no
existing Spec changes. A need the Spec cannot yet express is an **Extra**, a
Kubernetes object carried beside the Spec: reviewed by the operator while
hosting is managed, limited or refused under self-service. An Extra that two
Applications carry is what the Spec learns next.

The Spec is rendered in two halves. A Helm chart the platform owns makes
everything inside the Application's namespace; Argo CD runs it as a
multi-source Application, with the chart from the platform and the Spec and
Extras from the Application's repository, and the chart's `values.schema.json`
refuses a Spec that does not fit. kluster does the work outside the namespace,
in Plan Mode like every other kluster write: the database and its role, the
Gateway's allow list, the probe target and the Secrets, and later the Authos
client, the mail sender and DNS. These are written as reconcile functions, so
a controller can take them over when self-service needs one.

The contract is one contract from the start, operated as managed hosting.
While hosting is managed only tosak changes a Client's Spec; self-service
changes who may edit it, not its form. The enforcement self-service needs
(allowed kinds for Extras, quotas, no `LoadBalancer` or `NodePort`) is built
before the first Client gets write access.

Four rules come with the contract:

- **Isolation comes before a Client's code runs.** Every Application gets its
  own database role and its own Redis password, its namespace runs under Pod
  Security `restricted` with a ResourceQuota, and a NetworkPolicy enforcer
  applies a default deny. The enforcer is chosen in a separate design
  question. The private-repository credential in Argo CD is only as safe as
  the network around Argo CD, so no Client code runs before the enforcement
  does.
- **Client Secrets use the mechanism this repository already uses**
  ([ADR 0003](./0003-recoverability.md)): a Secret Catalog and SOPS ciphertext
  beside the Spec in the private repository, one Catalog per Registry entry.
  Keys the platform makes are `generated`; keys the Client owns are `issued`
  from the Client's own vault item or set by hand; a self-service Client
  encrypts with the public age key.
- **Deploys go through Argo CD Image Updater**, which writes the new image
  version back into the Spec's repository. A Client's only deploy input is an
  image push. tosak's own Applications keep the tag-bump pipeline until they
  move to the Registry, then use the same flow.
- **A Client's images stay in a registry the Client owns**, any OCI registry
  with GHCR in the Client's own account as the documented default. The Spec
  names the image and a pull credential, which is an `issued` Secret. The
  platform alerts on `ImagePullBackOff` and on an expired pull credential.

The Applications already in `projects/` move in a ladder where each step
proves one more part of the contract. tosak-net goes first, because it has one
component, no database and no Secrets: it proves the chart, the Registry
generator, the outside reconcile and the Image Updater write-back with no data
at risk. doma follows and proves `services.database`, including the change of
its database's owner from `tosak` to its own role on live data, with a restore
from the R2 backup ready. The first Client can go live any time after that
step and after the isolation work. Authos moves last: it has four components,
a keystore mounted as a file (an Extra), the Redis password to split, and
every application that signs in through it. imaps and wasteio are not running
and do not migrate; each comes back on the new model only if it is revived.
The two generators never render the same Application, and the old `projects`
generator is removed after Authos moves.

## Why this and not the alternatives

Self-service from the first day was set aside because nothing about the first
Client needs it, and the enforcement it requires is the largest piece of the
work. A managed-only format was set aside for the opposite reason: a second
format later would mean migrating every Client. One contract operated as
managed costs nothing extra now and leaves self-service as a change of who
edits.

Raw Kubernetes manifests per application would have been the smallest change,
and the reason not to take them is the doctrine: every application would carry
its own copy of the security context, the probes and the resource rules, and a
platform standard would change only by editing every copy. A pure Spec with no
escape would have blocked any application whose need the Spec cannot yet
express. The Spec with Extras keeps the standards in one place while no
application waits for the schema, and it makes the schema's next field visible.

A custom resource with an operator, Crossplane or kro could render the whole
Spec inside the cluster, and is the natural end state for self-service. It was
set aside for now: an operator is a large build before the first Client,
Crossplane's memory use is out of proportion to this cluster, and kro is
young. Crossplane is judged again in the
[Providers Plan](../plans/providers-the-platform-adapts-to-more-than-one.md),
where it would also manage compute. Letting kluster render every object was
also set aside, because generated files committed to Git drift from their
generator. The split keeps the inside declarative in Argo CD and the outside in
the tool that already does Plan Mode writes, written so a controller can
replace it.

One private repository for every Spec would have been simpler for Argo CD, but
tosak's own Applications would lose their public Specs, and self-service would
need a second mechanism anyway. Argo CD's SCM Provider generator, which finds
repositories by scanning for a file, was rejected because a scan admits any
repository that holds the file; admission has to be a decision.

For Secrets, keeping every value only in Bitwarden would make each generated
value a vault write, make a rebuild depend on the vault, and give a
self-service Client no way to supply its own values. OpenBao with the External
Secrets Operator would add a stateful service to run, unseal and back up. The
External Secrets Operator can still arrive later as a new `issued` source
without a change to the contract.

A general tag-bump for Clients would need a credential and branch rules for
every Client repository, and `deploy-scope` cannot prove which repository a
pull request came from. Running tag-bump for tosak's Applications and Image
Updater for Clients would be two flows for one job, which the doctrine rules
out. A registry hosted by the platform (Zot, or a Scaleway registry per
Client) is kept as a later platform service for Clients without one of their
own; nothing in the Spec changes when it arrives.

Putting identity and mail in Spec version 1 would make the Spec wait for two
reconcilers that do not exist and for the open question of how applications
send mail ([ADR 0012](./0012-mail-is-self-hosted-on-stalwart-and-sent-through-amazon-ses.md)).
A wide version 1 covering every shape in `projects/` today would roughly
double the schema, mostly for Stalwart, which is a platform service and does
not go through the Spec at all.

Moving the Client first would make a Client find the contract's faults, and
the Client's application has no date, so the platform would wait for it.
Moving everything in one cutover would put every Application behind one
change.

## What it costs

The build is real work before any Client benefits: the chart and its schema,
the Registry generator, the reconcile functions, kluster reading more than one
Secret Catalog, Image Updater, and a NetworkPolicy enforcer, which means a
change of CNI or an addition beside Flannel. Until Authos moves, two models
run side by side, and every change to how applications are deployed has to be
made in both.

One part is not proven: that Image Updater can write back into a values file
of a multi-source Helm Application. The rehearsal project proves it before
tosak-net moves; if it fails, the write-back target changes, not the contract.

Two migration steps touch live data. Changing a database's owner to a new role
can fail half-way, so doma and Authos each move with a tested restore from R2
ready.

Part of the platform's state stops being public. A reader of this repository
can no longer see which Applications run on the cluster, because the Registry
is private, and a Client's Spec is visible only to tosak and that Client.

While hosting is managed, every Extra costs the operator a review, and every
Client-owned registry is a dependency the platform does not control: an
expired pull credential stops that Client's next rollout, which is why the
alert is part of the decision.

## Amendments

**2026-11-15: an application incorporates the platform; it is not built
around it.** The operator set the rule after the decision: a Client's code
must not be shaped by tosak.net, and every platform service is optional. Four
changes to Spec version 1 follow from it. The Spec maps each value the
platform provides onto the application's own environment variable names, for
example `services.database.env: { DATABASE_URL: url }`, instead of handing
over a Secret with fixed key names. The port, the health paths and the size
are optional, and the platform's conventions fill them when they are absent.
The root filesystem stays read-only, `/tmp` stays writable by default, and the
Spec may name further writable paths. The smallest valid Spec is a name, one
component with an image, and one host; the `services` block and each of its
members are optional. What remains asked of the code is what any careful host
asks: run as a non-root user, keep no state on the local disk, and answer a
liveness path. Checking an application against these rules before it deploys
becomes a service of its own
([Contract validator](../plans/contract-validator-onboarding-proves-an-app-fits-the-platform.md)).
