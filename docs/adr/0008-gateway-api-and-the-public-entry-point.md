# 8. Gateway API replaces ingress-nginx, and the cluster gets one public entry point

Date: 2026-09-20

## Status

Accepted. Supersedes the ingress shape described in `README.md` and in the
`Active/CLAUDE.md` workspace doctrine, both of which still describe two
ingress-nginx classes.

## Context

**ingress-nginx was retired in March 2026.** There are no further releases, no
bugfixes, and no fixes for security vulnerabilities found from now on. SIG
Network and the Security Response Committee tell every user to migrate. InGate,
the planned successor controller, never reached maturity and was retired with
it.

The rebuild is the only cheap window. Nothing is installed, no traffic is live,
and the load balancer is already scheduled for deletion and recreation. Doing
this later means standing up an unmaintained controller, exposing it on a public
load balancer, and then migrating with live traffic on it.

Four questions were raised as blocking. Reading the repository answered two of
them differently from how they were posed, and answering them changed the shape
of the decision.

**The private class has no consumer.** `private-nginx` was cited as one of two
classes to reproduce, and its `hostNetwork` DaemonSet was the hardest thing to
reproduce in Gateway API. Its only two consumers in the repository are
`core/longhorn/longhorn-ingress.yaml`, deleted with Longhorn by ADR 0005, and
`core/whoami-test-ingress.yaml`, a test. Every real host uses `public-nginx`.
The private class exists to put the Longhorn UI on the VPN, and Longhorn is
gone.

**The rewrite annotations were never active.** `authos`, `imaps` and `wasteio`
carry `nginx.ingress.kubernetes.io/rewrite-target: /`, and the migration plan
called them load-bearing. Every path in all three objects is `/`, which equals
the rewrite target, and ingress-nginx skips the rewrite in exactly that case:

```go
// if the path in the ingress rule is equals to the target: no special rewrite
if path == location.Rewrite.Target {
    return defProxyPass
}
```

So no rewrite directive was ever emitted for those hosts. Gateway API passes the
path through unchanged by default, which is already the live behaviour.

**cert-manager's Gateway API support is not automatic.** The feature gate has
been enabled by default since 1.15, but the controller still requires an
explicit `--enable-gateway-api`, and the Gateway API CRDs must exist before it
starts. This is an install-order constraint, not a capability question.

**Every host is proxied by Cloudflare.** All five resolve to Cloudflare edge
addresses. Two things follow. Replacing the load balancer is an origin change
behind the proxy, invisible to visitors, so the four A-record updates are not a
propagation event. And the connection arriving at the load balancer comes from
Cloudflare, so PROXY protocol preserves a Cloudflare address and not the
visitor's. `core/ingress-controller/public/config.yaml` carried
`use-forwarded-headers: "true"` to handle this; nothing in the migration plan
carried it across.

## Decision

**The controller is Envoy Gateway. Flannel stays.**

Cilium ships a Gateway API implementation, and ADR 0001 reserves
`10.245.0.0/16` for a per-node Cilium migration, so one decision could have
settled CNI and Gateway together. It was rejected, for now:

- Cilium's Gateway API requires `kubeProxyReplacement=true` and `l7Proxy=true`.
  Adopting it is therefore three structural changes, not one — replace Flannel,
  replace kube-proxy, adopt a new Gateway — on a cluster that became Ready the
  same day.
- ADR 0001's own trigger has not fired. It names NetworkPolicy as the reason to
  move to Cilium and records that policy enforcement "buys nothing at the time
  of this decision". The cluster still has zero NetworkPolicy objects. Adopting
  a CNI to obtain a Gateway is adopting it for a reason that is not about the
  CNI.
- ADR 0006 lists six settings that silently point at the wrong interface if left
  alone. Cilium adds more of that class: it auto-detects devices on hosts
  carrying `eth0`, `enp7s0`, `wg0`, `flannel.1` and `cni0`, and the MTU chain
  proven end to end in Phase 3 would have to be proven again.

**`10.245.0.0/16` stays reserved and unspent.** This decision keeps the Cilium
option rather than consuming it. Cilium and Envoy Gateway coexist; if Cilium is
adopted later for NetworkPolicy, its Gateway implementation is optional.

**The private class is not rebuilt.** `core/ingress-controller/` is deleted
entirely, with `core/whoami-test-ingress.yaml`. A VPN-only host is built again
when a component needs one, against a real requirement.

**One shared Gateway, in a namespace named `gateway`.** It owns the listeners,
the certificate and the load balancer. Each application keeps its `HTTPRoute`
next to its workload and attaches through `allowedRoutes`. Rejected: one Gateway
per namespace, which yields one Envoy fleet, one Hetzner load balancer and one
public IP per application, where there is one public IP; and Envoy Gateway's
`mergeGateways`, which would keep certificates in application namespaces but is
a non-portable extension, and `ClientTrafficPolicy` is known not to apply
cleanly to every listener of a merged Gateway. PROXY protocol is mandatory here,
so that risk is not acceptable.

**ArgoCD gives up TLS passthrough.** `argocd-server` runs with `--insecure`, TLS
terminates at the Gateway, and `argocd.tosak.net` becomes an ordinary
`HTTPRoute`. Passthrough would require a `Gateway` listener in `Passthrough`
mode and a `TLSRoute`, which lives in the Gateway API **experimental channel**;
every other host needs only the standard channel. ArgoCD documents passthrough
as preferred because ingress-nginx broke its gRPC CLI when terminating — an
nginx limitation, not a general one. The `argocd-server` Service port carries
`appProtocol: kubernetes.io/h2c` so the Envoy data plane speaks HTTP/2 to the
backend and the `argocd` CLI keeps working. **This must be proven, not
assumed:** `argocd login` and `argocd app list` over the public host are an
acceptance test, and passthrough plus the experimental CRDs is the fallback if
they fail.

This also removes a coupling. Today cert-manager writes the public certificate
into `argocd-server-tls`, a secret another component reads and serves. Nothing
needs that now.

**No `URLRewrite` filters anywhere.** The three `rewrite-target` annotations
were inert. `ingress2gateway` will convert them into `ReplacePrefixMatch: /`
filters; those are deleted, not carried. The filter is harmless on a `/` prefix
and would break a real sub-path if copied — `/duster` on `authos-demo.tosak.net`
must reach Duster with its path intact, and under Gateway API that is the
default rather than something to configure.

**cert-manager uses DNS-01 through Cloudflare, with one wildcard certificate for
`*.tosak.net`.** The DNS convention is one subdomain level only, because
Cloudflare's free plan covers `*.tosak.net` and nothing deeper, so one
certificate covers every host the convention can produce. It issues before the
load balancer exists and before DNS is repointed, so the new entry point serves
valid TLS from its first request, and the Gateway needs one HTTPS listener
rather than five. HTTP-01 was rejected because it makes every certificate wait
on a load balancer that does not exist yet and on A records that still point at
the old one, and because the challenge would have to traverse the Cloudflare
proxy to reach the origin. The wildcard does not cover the apex `tosak.net`; no
host uses the apex.

The cost is a credential: a Cloudflare API token scoped to `Zone:DNS:Edit` on
`tosak.net` alone, encrypted with SOPS and age per ADR 0003.

**The real client address comes from `CF-Connecting-IP`, and the origin is
locked with Authenticated Origin Pulls.** `ClientTrafficPolicy` sets
`clientIPDetection.customHeader` to `CF-Connecting-IP`, and
`tls.clientValidation.caCertificateRefs` requires a client certificate that
Cloudflare presents. A header is only trustworthy if the connection that carried
it is, and this proves cryptographically that the connection came through
Cloudflare.

The certificate must be a **per-zone** one, uploaded to Cloudflare. Cloudflare's
shared origin-pull certificate is identical for every Cloudflare customer, so
validating it proves "some Cloudflare account" and not this one.

**An IP allowlist cannot do this job, and trying is worse than doing nothing.**
Envoy Gateway's `SecurityPolicy` `clientCIDRs` matches the *detected* client
address, which `clientIPDetection` defines. Allowlisting Cloudflare's ranges
while detecting from `CF-Connecting-IP` checks the forgeable header against
itself: anyone who finds the origin address sets the header to a Cloudflare
value, passes the allowlist, and spoofs the client address that Authos sees.
Hetzner offers no alternative, because its firewalls attach to servers and not
to load balancers.

**The load balancer is rebuilt, not adopted.** Load balancer `6579148` is
deleted and the CCM creates a fresh one, which proves the definition reproduces
from scratch. The hand-written `core/load-balancer/load-balancer.yaml` Service,
whose selector named ingress-nginx pods, is deleted: Envoy Gateway creates the
Service for each Gateway itself, so the Hetzner annotations move onto an
`EnvoyProxy` resource under `provider.kubernetes.envoyService.annotations`, and
PROXY protocol is accepted by `ClientTrafficPolicy.enableProxyProtocol`. **Both
halves must land together** — PROXY headers arriving at a listener that does not
expect them break every connection. `private-ipv4: 10.0.4.2` stays pinned, so
the load balancer keeps a known address in the load balancer subnet. The mqtt
service on port 1883 and the `tcp-services` ConfigMaps are dropped; wasteio is
out of scope under ADR 0003.

**Scope follows ADR 0003.** Five hosts are served: `argocd`, `authos`,
`authos-api`, `authos-demo`, `doma`. The `imaps` and `wasteio` routes are
converted and committed but not applied, matching how their manifests stay in
the repository unsynced.

## Consequences

- **`Ingress` is no longer the interface an application uses.** Every future
  application adds an `HTTPRoute`, not an `Ingress`. `CLAUDE.md` at the
  workspace root and this repository's `README.md` both document the old shape
  and are now wrong.
- **Certificates leave application namespaces.** A listener's certificate must
  be a Secret in the Gateway's namespace, so `authos-tls`, `doma-tls`,
  `authos-demo-tls` and `argocd-server-tls` are replaced by one wildcard secret
  in `gateway`. An application can no longer hold its own certificate, and the
  Gateway's `allowedRoutes` becomes the explicit statement of which namespaces
  may attach — a control `Ingress` never had, where any namespace could claim
  any host.
- **Two new credentials to manage and renew:** the Cloudflare DNS token, and the
  per-zone origin-pull certificate. Neither existed before. Both must be in the
  Phase 5 secret inventory.
- **The origin refuses direct connections.** Anything that reached a host by its
  origin address, bypassing Cloudflare, stops working. That is the intent, and
  it means debugging must go through Cloudflare or through the VPN.
- **Envoy Gateway is a second Envoy if Cilium is adopted later**, because
  Cilium's L7 proxy is also Envoy. That duplication is accepted as the price of
  keeping the two migrations separate.
- **The gRPC path is the one unproven claim in this record.** If `--insecure`
  plus h2c does not carry the `argocd` CLI, the fallback is passthrough, and
  passthrough costs the Gateway API experimental CRDs for one host.
