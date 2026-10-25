---
title: "The origin is locked: Cloudflare presents our own certificate and the Gateway requires it"
description: "How Authenticated Origin Pulls with our own certificate authority closed the origin in two staged pull requests, and what proved each stage."
type: log
topics: [security, ingress, networking]
date: 2026-10-25
---

Phase 4 of the [rebuild Plan](../runbook/rebuild-2026-09-20.md) held one item back on purpose: the origin lock. Until it was done, `CF-Connecting-IP` could be forged by anyone who found `77.42.14.48`. The lock is now on. Code is in PR #59 and PR #60, both merged on 2026-10-25.

## The decision

The operator chose Cloudflare zone-level Authenticated Origin Pulls with an own certificate, not Cloudflare's shared one. The shared certificate is the same for every Cloudflare customer, so it cannot tell our zone from any other. The operator uploads the leaf and turns the feature on in the dashboard. No new API token is needed, because the `Zone:DNS:Edit` token cannot change zone settings.

The operator made a certificate authority (RSA 4096, ten years, to 2036-10-22) and a leaf (RSA 4096, two years, to 2028-10-24, `CA:FALSE`, extended key usage `clientAuth`) with `openssl`, in their own terminal. Only the two public `.crt` files reached the agent; `openssl verify -CAfile ca.crt leaf.crt` returned OK and neither file held a key. Both private keys are kept in Bitwarden, profile `kluster`, as PEM in the notes: `kluster/cloudflare-aop-ca` and `kluster/cloudflare-aop-leaf`. The Gateway does not pin the leaf (`certificateHashes`); trusting the CA is enough.

## Two stages

The lock went on in two steps, so that a mistake could not break every host at once.

1. PR #59 added `core/gateway/cloudflare-aop-ca.yaml` (ConfigMap `cloudflare-aop-ca` in `gateway`, key `ca.crt`, the public CA) and `tls.clientValidation` with `optional: true` in `core/gateway/client-traffic-policy.yaml`. `core-gateway` synced it; the policy showed `Accepted=True` and the hosts did not change.
2. The operator uploaded the leaf in the dashboard (zone level, upload certificate) and switched zone-level Authenticated Origin Pulls on.
3. Cloudflare was proven to present the leaf. Then PR #60 replaced `optional: true` with `mode: RequireAndVerify`.

## What was found

Envoy Gateway v1.9.1 deprecates `clientValidation.optional`. The policy status after PR #59 carried `Warning=True DeprecatedField: spec.tls.clientValidation.optional is deprecated, use spec.tls.clientValidation.mode instead`. The `mode` values are `Request`, `RequireAny`, `VerifyIfGiven` and `RequireAndVerify` (the default). [ADR 0008](../adr/0008-gateway-api-and-the-public-entry-point.md) and the Plan were written with the older field; the policy file now uses `mode`. To open the lock again, use `mode: VerifyIfGiven`, never no validation.

Cloudflare's documentation for zone-level set-up with an own certificate adds three rules: upload a leaf with its key (a root CA is refused with "missing leaf certificate"); the origin trusts the CA that signed it; turning the feature on is a separate toggle after the upload. The zone SSL mode must be Full or higher. Cloudflare emails 30 and 14 days before the leaf expires.

## Proof

- **Cloudflare presents the leaf** (with `optional: true` and the feature on). On the 443 listener `listener.0.0.0.0_10443` of both Envoy pods, `ssl.handshake` rose 14147 to 14151 and 14183 to 14187, while `ssl.no_certificate` stayed at 14147 and 14183 and `fail_verify_error` stayed 0. As a control, one direct request to `77.42.14.48` without a certificate returned 200 and raised `no_certificate` from 14147 to 14148, so the counter does see a client without one. The same result proves the zone SSL mode is at least Full.
- **The lock holds** (after PR #60 synced, `Accepted=True`). Through Cloudflare, argocd, authos, authos-demo and doma returned 200. authos-api returned 404 `application/json` at `/`, the application's own answer (no root page, no actuator), as before the lock. A direct `curl --resolve doma.tosak.net:443:77.42.14.48 https://doma.tosak.net/` failed with `tlsv13 alert certificate required`, and `ssl.fail_verify_no_cert` rose 0 to 1 on one pod; `fail_verify_error` stayed 0.
- **Port 80** is not locked and need not be: a direct `http://` request to `77.42.14.48` returned 301 to HTTPS, and that listener serves only the redirect route.
- **IPv6** (`2a01:4f9:c01e:39a::1`) was not tested, because the laptop has no IPv6 route (`curl: (7) Couldn't connect`). Assumed locked: the same listener serves both families.

The Envoy proxy image has no `curl`. Read the admin statistics with `kubectl port-forward` to port 19000 of each Envoy pod, then `curl http://127.0.0.1:<port>/stats`.

## Consequences

- A rehearsal Gateway now refuses every HTTPS client: `core/gateway` is synced to the rehearsal unchanged and rehearsal hosts are not behind Cloudflare. This is inferred from the configuration and not tested; no rehearsal step tests HTTPS through the Gateway today. If one ever does, it needs a rehearsal cut of `core/gateway` without `clientValidation`.
- Rollback is a revert in git. ArgoCD reads git directly and does not depend on the Gateway, so a broken lock cannot lock ArgoCD out. This is by design and was not tested.
- The leaf expires on 2028-10-24; the steps to renew it are in [Renew the Cloudflare origin-pull leaf certificate](../runbook/renew-the-cloudflare-origin-pull-leaf-certificate.md). The CA changes only by 2036.
- Phase 4 still owes the apex and `www` decision (tied to the deferred DNS-writes decision) and monitoring, which waits for its own scope grill. The first cert-manager renewal of the wildcard, on 2026-11-19, is untested.
