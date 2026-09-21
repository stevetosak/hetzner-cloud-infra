# core/gateway-api — the Gateway API types

`crds.yaml` is upstream `standard-install.yaml` **v1.6.2**, byte for byte. It
is not rendered and not edited. To upgrade, download the new
`standard-install.yaml` from the `kubernetes-sigs/gateway-api` release and
replace the file.

**Standard channel only (ADR 0008).** Nothing in this cluster's design needs
the experimental channel.

## Why this is separate from Envoy Gateway

Two components need these types, and they need them at different moments:

- **Envoy Gateway** implements them.
- **cert-manager** refuses its Gateway API feature if the types are absent when
  it starts.

So the order is **these CRDs, then Envoy Gateway, then cert-manager**.

Keeping them here also keeps the version explicit. The Envoy Gateway chart
would install its own copy, and it installs the **experimental** channel by
default. See `core/gateway/README.md`.

## The bundle carries its own guard

`standard-install.yaml` includes a `ValidatingAdmissionPolicy`,
`safe-upgrades.gateway.networking.k8s.io`, with `failurePolicy: Fail` and
`validationActions: ["Deny"]`. It denies any CRD write that would put
experimental channel types on top of standard ones, and any bundle older than
v1.5.0. ADR 0008's rule is therefore enforced by the cluster.

🔴 **It has a blind spot.** The policy tests
`object.spec.group != 'gateway.networking.k8s.io'`. The experimental channel
also ships `xbackends`, `xbackendtrafficpolicies` and `xmeshes` in group
**`gateway.networking.x-k8s.io`**, which the policy does not match. Those
three install without complaint. `core/gateway/strip-gatewayapi-crds.py` is
what keeps them out.

## Apply

```sh
kubectl apply --server-side --field-manager=cloud-infra -f core/gateway-api/crds.yaml
```

`--server-side` is not optional. These CRDs are large, and a client-side apply
writes the whole object into the `last-applied-configuration` annotation,
which overflows the 256 KiB annotation limit.

## Version note

v1.6.2 is used, not the v1.6.1 that Envoy Gateway v1.9.1 vendors. The two were
diffed before choosing: the differences are the `bundle-version` annotations
and one `description` string about redirect status codes. No schema changes.
