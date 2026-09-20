# core/gateway — the Public Entry Point

Envoy Gateway and the one shared `Gateway` that every public host attaches to
(ADR 0008). This replaces `core/ingress-controller/` and
`core/load-balancer/`, both deleted.

| File | What it is |
|---|---|
| `namespaces.yaml` | `envoy-gateway-system` and `gateway`. Neither chart makes its own. |
| `envoy-gateway.yaml` | Envoy Gateway **v1.9.1**, rendered. Do not edit in place. |
| `envoy-gateway-values.yaml` | The values that produced it. |
| `render.sh` | Renders `envoy-gateway.yaml`. |
| `strip-gatewayapi-crds.py` | The filter `render.sh` pipes through. Read the next section before deleting it. |
| `certificate.yaml` | The one wildcard certificate the listeners serve. |

## 🔴 The chart installs the EXPERIMENTAL Gateway API channel by default

`crds.gatewayAPI.channel` defaults to `experimental` in the chart, and the CRDs
live in Helm's `crds/` directory, where **no value can deselect them** because
`crds/` is not templated. ADR 0008 allows the standard channel only, and
`core/gateway-api/crds.yaml` installs it.

So `render.sh` strips every Gateway API CRD out of the rendered output
afterwards. `strip-gatewayapi-crds.py` does that on parsed documents, not on
text, and **fails if it removes nothing** — because that means the chart
changed shape and the assumption behind the filter no longer holds.

The filter removes two groups, and the second one is easy to miss:

- `gateway.networking.k8s.io` — the ten standard kinds.
- `gateway.networking.x-k8s.io` — `xbackends`, `xbackendtrafficpolicies`,
  `xmeshes`. **The upstream safe-upgrade admission policy does NOT catch
  these**, because its CEL expression tests the `k8s.io` group alone. The
  first version of the filter missed them too, and three experimental CRDs
  survived into the rendered file. This filter is the only thing keeping them
  out of the cluster.

The safe-upgrade policy is also stripped: the upstream standard bundle already
owns it, and two components managing one cluster-scoped object fight.

## 🔴 `helm template` corrupts an oci:// render on a cold cache

`helm template` against an `oci://` chart prints `Pulled:` and `Digest:` on
**stdout** the first time it fetches the chart. Those two lines land inside the
manifest. It only happens when the chart cache is cold, so the corruption is
intermittent and a re-render appears to fix it.

`render.sh` pulls the chart to a temporary directory first and renders from
there. The digest it prints is recorded in the rendered file's header, which is
stronger provenance than the version tag alone.

## Apply order

The order is not free (ADR 0008):

```sh
kubectl apply --server-side --field-manager=cloud-infra -f core/gateway-api/crds.yaml
kubectl apply --server-side --field-manager=cloud-infra -f core/gateway/namespaces.yaml
kubectl apply --server-side --field-manager=cloud-infra -f core/gateway/envoy-gateway.yaml
# then cert-manager, which refuses its Gateway API feature without the CRDs
```

The chart creates no `GatewayClass`. This repository declares one, so the
controller name is visible in git rather than implied by a Helm release.
