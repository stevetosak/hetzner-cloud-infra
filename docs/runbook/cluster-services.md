# Cluster services — build runbook

Written verbatim as each command ran, on 2026-09-21 (ADR 0004). This file is
the deliverable of Phase 4.

Read `docs/runbook/rebuild-2026-09-20.md` for the surrounding plan,
`docs/adr/0008` for every decision about the Public Entry Point, and
`docs/runbook/workers.md` for the cluster this builds on.

**Conventions in this file.** A fenced block is a command that was run, exactly
as it was run. Output is quoted only where it was used as evidence. Every
claim about Hetzner or Cloudflare state was checked against that provider's
API, never read from a command's own output.

---

## 0. Starting state

| | |
|---|---|
| Nodes | 4 `Ready`, v1.37.0 — `k8s-cp`, `k8swk1`, `k8swk2`, `k8swk3` |
| Pods | 20 of 20 `Running` |
| Namespaces | `default`, `kube-flannel`, `kube-node-lease`, `kube-public`, `kube-system` |
| Load balancer | `6579148`, `authos-lb`, public `77.42.14.48`, private `10.0.4.2`, 0 targets |
| StorageClass | `hcloud-volumes (default)` |

Nothing from Phase 4 is installed.

### Four corrections to ADR 0008 and to the Phase 4 checklist

All four were found by reading the upstream charts and the Cloudflare zone
before anything was applied. Each one changed the work.

1. **`TLSRoute` is in the Gateway API STANDARD channel** as of v1.6, together
   with `TCPRoute`, `UDPRoute` and `ListenerSet`. Verified against the
   upstream `standard-install.yaml` for both v1.6.1 and v1.6.2. ADR 0008 says
   the ArgoCD passthrough fallback "costs the experimental CRDs". It no longer
   does. This does not change the decision to terminate TLS at the Gateway, but
   it makes the fallback cheap.
2. **The cert-manager chart option is `config.gatewayAPI.enabled`**, not
   `config.enableGatewayAPI` as ADR 0008 and the checklist both name it. The
   `ExperimentalGatewayAPISupport` feature gate has defaulted to true since
   1.15, so the gate was never the missing piece. The chart's `crds.enabled`
   also defaults to **false**.
3. **The old load balancer holds `10.0.4.2`**, the exact private address the
   `EnvoyProxy` pins. So deleting `6579148` is a PREREQUISITE of the Gateway,
   not a cleanup step after it. The checklist lists the delete after the
   Gateway. That order cannot work.
4. **The zone holds twelve A records, not four**, and one of them is the apex.
   See the Cloudflare step.

---

## 1. Gateway API CRDs — standard channel

ADR 0008 allows the standard channel only.

`core/gateway-api/crds.yaml` is upstream `standard-install.yaml` **v1.6.2**,
unmodified. v1.6.2 was chosen over the v1.6.1 that Envoy Gateway v1.9.1
vendors, after diffing the two: the only differences are the version
annotations and one description string. There is **no schema change**.

```
kubectl apply --server-side --field-manager=cloud-infra -f core/gateway-api/crds.yaml
```

### Verified over the API

```
kubectl get crd -o json | ... group contains "gateway"
```

All ten CRDs report `channel: standard`, `bundle-version: v1.6.2`:
`backendtlspolicies`, `gatewayclasses`, `gateways`, `grpcroutes`,
`httproutes`, `listenersets`, `referencegrants`, `tcproutes`, `tlsroutes`,
`udproutes`.

### The bundle installs its own guard, and the guard has a blind spot

The standard bundle also carries a `ValidatingAdmissionPolicy` named
`safe-upgrades.gateway.networking.k8s.io`, with `failurePolicy: Fail` and
`validationActions: ["Deny"]`. It refuses any attempt to install experimental
channel CRDs on top of standard ones. That makes ADR 0008's "standard channel
only" rule enforced by the cluster and not only by this document.

🔴 **The guard does not cover everything.** Its CEL expression tests
`object.spec.group != 'gateway.networking.k8s.io'`, so it sees nothing in
group **`gateway.networking.x-k8s.io`** — which is where the experimental
channel puts `xbackends`, `xbackendtrafficpolicies` and `xmeshes`. Those three
would install silently. See the Envoy Gateway step, where that mattered.
