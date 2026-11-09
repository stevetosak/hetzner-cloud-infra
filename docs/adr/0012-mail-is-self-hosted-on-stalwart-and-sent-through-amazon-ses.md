---
title: "Mail is self-hosted on Stalwart and sent through Amazon SES"
description: "Why the inboxes for tosak.net and later for client domains run on Stalwart in the cluster, why every outgoing message leaves through Amazon SES, where the mail is stored and backed up, and how mail reaches the cluster without sharing the web entry point."
type: decision
status: accepted
date: 2026-11-02
topics: [networking, storage, backups, security]
---

The platform has no mail. `tosak.net` has no inbox, so even the reports that a
mail setup asks for (DMARC and TLS reports) have nowhere to go. Client domains
are expected to want inboxes as well, and the applications will need to send
mail. A hosted mail provider would answer the first need at once; the operator
chose to run the inboxes on the platform instead, for three reasons: full
control over the data and the accounts, room to grow to client domains without
a per-mailbox fee, and the learning that comes from running it.

Running mail has two halves of very different difficulty. Receiving and storing
mail is an ordinary server problem: a process, a store, a backup. Sending is
not. A receiver judges a message by the reputation of the IP address that sent
it, and an address in a cloud provider's range starts with none that helps
(assumed from how large receivers are known to filter; not measured here).
Building that reputation by hand is slow and a single bad week can undo it.

Several parts of the design are hard to reverse once mail flows. The storage
layout holds every message ever received, so moving it later is a migration.
The DNS records for a client domain live in the client's zone, so every change
there needs the client. And the way mail enters the cluster decides which
public IP address the MX record publishes. The choices were made in a design
review on 2026-11-01 and 2026-11-02, one question at a time, each answer the
operator's.

## The decision

**Stalwart holds the inboxes in the cluster, and every outgoing message leaves
through Amazon SES.** Stalwart (Community edition, AGPL) runs as one pod in the
namespace `mail`, from the image `stalwartlabs/stalwart` pinned by digest. It
receives mail for its domains, stores it, and serves it to mail apps over IMAP
(993) and submission (465). Anything addressed outside its own domains it hands
to the SES SMTP endpoint in `eu-central-1` on port 587; it never delivers to a
foreign MX itself. `tosak.net` comes first, then client domains, then the
applications' own mail.

Stalwart splits its storage into kinds, and each kind goes where an existing
mechanism already protects it. The **data store** (accounts, folders, message
metadata, the queue) is a database `mail` on the shared CloudNativePG cluster
`tosak-pg-cluster`, owned by the role `tosak` like every other database
([ADR 0007](./0007-shared-infrastructure-is-named-for-the-organisation.md)). It inherits the
barman backups to R2 and the monthly Restore Drill with no new machinery. The
**blob store** (the message files themselves) is a Backblaze B2 bucket in an EU
region, with versioning on and old versions expiring after 30 days, the same
window as the PostgreSQL backups. A restore reads the bucket as of a point in
time (`rclone --b2-version-at`), so the blobs can be matched to the moment the
database restore reaches. The in-memory store stays inside the process. The pod
has no volume.

Mail enters through **its own Hetzner load balancer**, an `lb11` with its own
public IP address, created by a `LoadBalancer` Service in `mail` for ports 25,
465 and 993. The load balancer sends PROXY protocol, and Stalwart reads it
directly, trusting it only from the load balancer's source range, so the
client's real address reaches the spam checks. This changes the "one public
entry point" of [ADR 0008](./0008-gateway-api-and-the-public-entry-point.md);
its amendment of 2026-11-02 records how. The certificate for `mail.tosak.net`
is a cert-manager `Certificate` from `letsencrypt-prod` by DNS-01, mounted into
the pod as a Secret, and Reloader restarts the pod after each renewal. The
Cloudflare token stays inside cert-manager.

Outgoing mail is authenticated by SES. Each domain uses **SES Easy DKIM**
(three CNAME records; SES signs and rotates the keys) and a **custom MAIL FROM
domain**, `bounce.<domain>`, whose MX points to
`feedback-smtp.eu-central-1.amazonses.com` and whose SPF record allows SES. The
envelope sender then sits under the domain itself, so SPF aligns with the
visible sender for DMARC, and bounces still reach SES, which counts them. If
the MAIL FROM records ever fail, SES falls back to its own `amazonses.com`
envelope (`UseDefaultValue`) rather than refusing to send; DMARC then still
passes on DKIM alone. Stalwart does not sign outgoing mail. Each domain's DMARC
record starts at `p=none` and is tightened by the operator from the reports.

For `tosak.net` the receiving side is also protected against downgrade:
**MTA-STS** tells sending servers to require a valid certificate on
`mail.tosak.net`. The policy file is served by the shared Gateway from a
ConfigMap in git, with an `HTTPRouteFilter` direct response, at
`mta-sts.tosak.net`, so no Stalwart HTTP path is public for it. The policy
starts in mode `testing`; the operator switches it to `enforce` once the TLS
reports (TLS-RPT, to `dmarc@tosak.net`) show no failures. Mail apps find the
server through SRV records (`_imaps._tcp`, `_submissions._tcp`); there are no
autoconfig or autodiscover hosts.

**The monthly Restore Drill gets a mail phase.** After the PostgreSQL restore,
the drill copies the B2 blobs as of the replay end into a scratch store, starts
a throw-away Stalwart on the restored `mail` database and that copy, and reads
messages back over IMAP. The design of that phase and its safety rules are
recorded in the amendment of 2026-11-02 to
[ADR 0003](./0003-recoverability.md).

## Why this and not the alternatives

The first question was whether to send from the cluster at all. Sending
directly from Stalwart would have kept every part of the system in the
repository, but it puts the platform's reputation on one cloud IP address with
no history, and it makes blocklist monitoring and IP warm-up a standing job.
SES sends from addresses that already carry a reputation, reports bounces and
complaints, and costs per message. The receiving half has no such problem and
stays on the platform, which is where the control and the learning are. A
fully hosted mailbox provider would have removed both halves of the work, and
with them the reasons the operator gave for the project.

For the data store, Stalwart's default RocksDB on a volume was rejected because
nothing backs up a volume in this cluster
([ADR 0005](./0005-storage-and-worker-identity.md) removed Longhorn, and the
Hetzner CSI driver has no snapshots), so mail would be the one data set
outside the recoverability design. Putting the blobs into PostgreSQL as well
was rejected: messages are large and grow without end, and every daily base
backup is a full copy, so the backups, the WAL and each drill restore would
grow with the mail instead of with the metadata.

For the blob store, Cloudflare R2 already holds the PostgreSQL backups, but it
has no object versioning, so a deleted or overwritten message could not be read
back as of a point in time without a copy-and-prune job of our own. Hetzner
Object Storage has versioning but is the same provider as the cluster, a single
failure domain with the thing it protects, and starts at about €6.49 a month.
B2 has versioning with lifecycle expiry, sits with a third provider, and is
billed by the gigabyte.

For inbound, `TCPRoute`s on the shared Gateway were possible, since the CRDs
are in the standard channel. They were rejected for two reasons. The MX record
must publish a real IP address, which on the shared load balancer is the web
origin's address that Cloudflare otherwise hides, and SMTP floods would land on
the same Envoy that serves every web host. A `hostPort` on a node was rejected
because it ties mail to one node and skips the load balancer's health checks.

For the certificate, Stalwart's own ACME client was rejected in both forms.
DNS-01 would put a token that can edit the zone into a pod that faces the
internet on port 25. TLS-ALPN-01 would need port 443 open on the mail load
balancer, with Stalwart's web interface behind it. cert-manager already holds
the token and already renews the wildcard.

For signing, Stalwart signing its own DKIM was rejected: it adds a key rotation
to run by hand, and SES does not check that signature. Signing twice adds the
same work for no gain. For MTA-STS, serving the policy from Stalwart would have
made a Stalwart HTTP path public; a static file in git, served by the Gateway,
changes only by review.

## What it costs

A second load balancer: one more `lb11` at about €5.39 a month (the price in
these docs; a rise in 2026 is reported and the Plan confirms it). The B2
account, the SES account and its production access are three outside
dependencies, each with its own credentials and its own account to keep alive.
SES charges per message, and its account can be paused if the bounce or
complaint rate climbs, which would stop all outgoing mail at once; CloudWatch
alarms on both rates watch for that. The data store adds load to the shared
PostgreSQL cluster, and one Stalwart pod means a restart (a renewal, a node
drain) refuses connections for about a minute; sending servers retry, so no
mail is lost (assumed from SMTP's retry rules; the rehearsal proves it).

`mta-sts.tosak.net` breaks the naming rule `<project>[-<service>].tosak.net`,
because RFC 8461 fixes the name. It is the only exception, and ADR 0008's
amendment records it.

Some choices were deferred, not rejected, and the operator decides each one;
none of them starts on a condition by itself. A second Stalwart pod is worth
raising once a paying client has mailboxes; it needs the in-memory store moved
to the shared Redis, and before that a check that Stalwart sets a TTL on every
key, because that Redis evicts only keys that have one and Authos shares it.
Stalwart Enterprise (tenants, per-tenant admins) is a license key on the same
binary, with no data migration. Webmail, MTA-STS for client domains and DANE
are left out for now. The choice as a whole is worth revisiting if SES's terms
or prices change, if B2 stops offering an EU region, or if the mail outgrows
one pod and the shared database.

## Amendments

**2026-11-07 — the second load balancer costs €7.49 a month.** The cost above
gives €5.39 for an `lb11`. Hetzner's load balancer page, read on 2026-11-07,
lists €7.49 a month (€0.0120 an hour) excluding VAT, the same in every
location; the rise came with Hetzner's price change of 2026-04-01. The choice
of a separate load balancer stands: the reasons against `TCPRoute`s on the
shared Gateway do not depend on the price.

The same check confirmed the rest of the design on the release it will run,
Stalwart 1.0.3 (released 2026-11-04), from its documentation and the source at
that tag. The certificate and key are read from files mounted from the
cert-manager Secret, so Stalwart's own ACME client stays unused; a renewed file
is read again only after a reload or a restart, which Reloader already does.
PostgreSQL holds the data store, B2 holds the blobs through its S3 API, and the
in-memory store stays in the process as long as no cluster coordinator is set.

**2026-11-08 — the blob store is Scaleway Object Storage, not B2.** The
decision above puts the message files in a Backblaze B2 bucket in an EU region,
and names "if B2 stops offering an EU region" as a reason to review the choice.
That is what happened. Backblaze's data-regions page still lists EU Central as
a sign-up choice, but the live sign-up form offered the operator only CA East,
US East and US West on 2026-11-08. The region is fixed per account, so the
form was not submitted; the cause is not known, and a question to Backblaze
support is open.

The blobs go to **Scaleway Object Storage in `nl-ams`, Standard Multi-AZ**,
which spreads each object over three availability zones. It meets every
criterion the B2 choice was made on, read from Scaleway's documentation source
on 2026-11-08: an EU region, versioning with `ListObjectVersions` and delete
markers as in S3, a lifecycle rule `NoncurrentVersionExpiration` with
`NoncurrentDays: 30` (rules run once a day and act within 24 hours), a
provider other than the cluster's, billing by use, and the S3 API that
Stalwart's blob store and rclone already speak. The point-in-time read becomes
`rclone --s3-version-at`, the S3 backend's form of the same flag. Scaleway
takes B2's place as the one new outside account, and the data stays under EU
law.

The other candidates were weighed against the same criteria. Cloudflare R2
still implements neither `PutBucketVersioning` nor `GetBucketVersioning`
(its S3 API page, 2026-10-14), so its rejection stands. Hetzner Object Storage
is still the cluster's own provider. Amazon S3 in `eu-central-1` meets every
criterion and needs no new account, since the SES account could hold it, but
costs about $0.0245 per GB-month (a third-party figure, not verified), more
per gigabyte than Scaleway. OVHcloud and IDrive e2 support expiry of old
versions too; IDrive bills at least one terabyte.

Scaleway's IAM grants Object Storage per Project, not per bucket. So each
bucket gets its own Project, `mail` for the live bucket and `mail-rehearsal`
for the rehearsal's, and each key is an application with
`ObjectStorageFullAccess` or `ObjectStorageReadOnly` scoped to one Project.
That gives four keys scoped to one bucket each with no bucket policy: read and
write for Stalwart and read only for the Restore Drill, for both buckets. The
rehearsal needs its own bucket and keys because kluster refuses an issued key
that has no rehearsal item
([ADR 0009](./0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md)).

The cost changes with it. Scaleway's public catalogue lists Standard
Multi-AZ at €0.000022 per GB without naming the time unit; read as per hour,
that is about €0.016 per GB-month, and egress to the internet costs €0.01 per
GB. The figure is assumed until the first invoice confirms it. Old versions
are billed as stored data until they expire, as they would have been on B2.
