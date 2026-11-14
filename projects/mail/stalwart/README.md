# mail stalwart

The mail server for `tosak.net` (ADR 0012): Stalwart 1.0 receives mail on 25,
takes submissions on 465 and serves IMAP on 993; every outgoing message leaves
through the relay route `ses` to Amazon SES. The data store is the database
`mail` on the shared PostgreSQL cluster, and the message files are in the
Scaleway bucket `tosak-mail-blobs`. The pod keeps no volume.

The image is Stalwart's own (`stalwartlabs/stalwart`), pinned by digest in
`manifests/base/deployment.yaml`; no CI bumps it, so an upgrade is a reviewed
change of that line. One replica with `Recreate`: each process leases a node
ID in the data store, and two must never overlap.

## Settings

Stalwart 1.0 reads only the data store from a file
(`manifests/base/config/config.json`). Every other setting lives in the data
store. The settings are the NDJSON plan `manifests/base/config/settings.ndjson`,
applied with `stalwart-cli apply` by the init container `apply-settings` on
every start:

1. `apply-settings.sh` puts two plain values into the plan: `S3_BUCKET` from
   the ConfigMap `stalwart-env` and the SES SMTP user name from the Secret
   `ses-smtp`. Each must match `[A-Za-z0-9._-]+`, and no `${...}` may remain.
   Every secret value stays a file reference in the plan.
2. It starts Stalwart in recovery mode (only the HTTP listener on 8080),
   applies the plan and stops Stalwart again.
3. The main container then starts normally.

The order matters. On an empty data store, a normal start inserts Stalwart's
defaults for every settings type that is still empty: seven listeners, a
direct `mx` route, and a log file on the read-only disk. Recovery mode inserts
none of them, so the plan is in place before the first normal start. A change
made in the web UI is undone at the next restart; change the plan instead.
The ConfigMap name carries a content hash, so a changed plan restarts the pod.

The plan uses `reconcile` for the listeners, routes, TLS strategies, tracers
and the certificate, so it also removes an object that is not in it. A domain
is only upserted: removing one from the plan never deletes its mailboxes.

What the plan sets:

- the listeners 25 (STARTTLS), 465 and 993 (implicit TLS) and 8080 (HTTP:
  health, metrics, admin), nothing else;
- the routes `local` and `ses`, and the outbound `route` expression: a local
  domain goes to `local`, everything else to `ses`. There is no `mx` route, so
  Stalwart never delivers to a foreign MX;
- the TLS strategy `ses`: STARTTLS required with a valid certificate, and no
  fallback to the default `invalid-tls` strategy on a retry. Without it, a
  missing STARTTLS would send the SES credentials in clear text;
- the domain `tosak.net` with DKIM management `Manual`: SES signs with Easy
  DKIM, Stalwart signs nothing;
- the blob store `S3` on Scaleway `nl-ams` (path style), a stdout tracer, the
  Prometheus exporter with basic auth, and ASN lookups `Disabled`.

On a fresh data store the first normal start still inserts the default ASN
source and downloads its data once, although the plan applied `Disabled` just
before; from the next start on, `Disabled` holds (seen in the lab, cause not
traced).

## The mail load balancer

`manifests/service.yaml` is the `LoadBalancer` Service for 25, 465 and 993:
its own Hetzner `lb11` `mail-lb`, reaching the nodes over the Private Network
from the fixed address `10.0.4.3`, with PROXY protocol on. It is applied by
hand, because applying it buys the load balancer:

```bash
kubectl apply -f projects/mail/stalwart/manifests/service.yaml
```

`externalTrafficPolicy: Local` keeps the load balancer's address as the source
the pod sees, so the listeners for 25, 465 and 993 trust a PROXY header from
`10.0.4.3/32` only (`overrideProxyTrustedNetworks` in the plan). From there, a
connection without a header is closed. A header error is only logged, and
never counts towards auto-ban, so the load balancer's TCP health checks do not
ban it. The address and the PROXY setting are pairs: change each half together.

## The certificate

`manifests/base/certificate.yaml` is the cert-manager `Certificate` for
`mail.tosak.net` (ECDSA P-256, `letsencrypt-prod`, DNS-01), synced by Argo CD
with the Deployment. Its Secret `mail-tls` is mounted at `/run/secrets/tls`;
Stalwart reads every certificate in `tls.crt`, so it serves the leaf with the
intermediate. Stalwart reads the files only at start, and Reloader restarts
the pod after each renewal. The pod cannot start before the Secret exists.

## Logins

`STALWART_RECOVERY_ADMIN` (`admin` and the Secret `admin`) is the recovery
login and stays valid in normal mode: it is the permanent admin login, used by
the init container and by the web UI behind Cloudflare Access.

## Lab proof (2026-11-14)

In a local Docker lab with the same security context (UID 2000, `drop: ALL`
plus `NET_BIND_SERVICE`, no privilege escalation, read-only root,
`ip_unprivileged_port_start=1024`): the init run on an empty database applied
the plan, a second run created and destroyed nothing, and the normal start
opened 25, 465, 993 and 8080 only. A message to a local account was stored in
the S3 bucket and read back over IMAP. A message to an outside address went to
the relay route; the relay's certificate was not valid, so Stalwart held the
message for a retry instead of sending it without TLS. The metrics answered
401 without the password and 200 with it. With a lab address in place of
10.0.4.3: a trusted client's PROXY header gave the session the claimed
address, a trusted client without a header got no greeting, and an untrusted
client's header was not read. Not proven there: a real SES delivery, the
Scaleway endpoint, the image volume on the cluster, and that the Hetzner load
balancer reaches the pod from 10.0.4.3; the rehearsal proves them.
