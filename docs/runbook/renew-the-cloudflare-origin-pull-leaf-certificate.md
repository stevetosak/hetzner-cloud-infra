---
title: "Renew the Cloudflare origin-pull leaf certificate"
description: "How to replace the client certificate Cloudflare presents to the Gateway before it expires, with no change in git, and how to prove the new one works."
type: procedure
topics: [security, ingress, secrets]
---

Cloudflare presents a leaf certificate to the Gateway on every request ([the Log](../logs/2026-10-25-the-origin-is-locked-cloudflare-presents-our-own-certificate-and-the-gateway-requires-it.md) tells how the lock was built). The current leaf expires on 2028-10-24; Cloudflare emails 30 and 14 days before. Run this procedure in the operator's own terminal, because it needs the CA private key. Nothing in git changes: the Gateway trusts the CA, and the CA is valid until 2036-10-22.

## Steps

1. Read the CA certificate and key from Bitwarden, profile `kluster`, item `kluster/cloudflare-aop-ca` (PEM in the notes). Write them to a private temporary directory, never to the repository.
2. Make a new RSA 4096 key and certificate signing request, and sign it with the CA for two years with `CA:FALSE` and extended key usage `clientAuth`. These are the commands that made the first leaf on 2026-10-25, run from that directory with `ca.crt` and `ca.key` in it; `umask 077` keeps the keys private, and nothing prints a key:

   ```sh
   umask 077
   openssl req -newkey rsa:4096 -nodes -keyout leaf.key -out leaf.csr \
     -subj "/CN=tosak.net Cloudflare origin pull" 2>/dev/null
   printf 'basicConstraints=CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=clientAuth\n' > leaf.ext
   openssl x509 -req -in leaf.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
     -out leaf.crt -days 730 -extfile leaf.ext 2>/dev/null
   ```
3. Run `openssl verify -CAfile ca.crt leaf.crt`. It must print OK.
4. Store the new key and certificate in Bitwarden, item `kluster/cloudflare-aop-leaf`, as PEM in the notes.
5. In the Cloudflare dashboard, upload the new leaf at zone level with its key. Cloudflare refuses a root CA ("missing leaf certificate"), so upload the leaf only.
6. Delete the old leaf in the dashboard. Keep zone-level Authenticated Origin Pulls on.
7. Delete the temporary directory.

## How to know it worked

Read the admin statistics of each Envoy pod in `envoy-gateway-system` with `kubectl port-forward` to port 19000 and `curl http://127.0.0.1:<port>/stats` (the proxy image has no `curl`). Note `ssl.handshake`, `ssl.no_certificate`, `ssl.fail_verify_no_cert` and `ssl.fail_verify_error` for the listener `listener.0.0.0.0_10443`. Send a few requests through Cloudflare. Then `ssl.handshake` must rise while `fail_verify_error` and `fail_verify_no_cert` stay where they were, and the hosts answer as before. A direct request to `77.42.14.48` must still fail with `tlsv13 alert certificate required`.

If the hosts break, upload the old leaf again if it has not expired. The git rollback is a revert of `mode: RequireAndVerify` to `mode: VerifyIfGiven`, never to no validation.
