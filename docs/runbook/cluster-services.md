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

---

## 2. Envoy Gateway

`core/gateway/envoy-gateway.yaml` is chart `gateway-helm` **v1.9.1**, rendered
by `core/gateway/render.sh`. The chart creates no namespace, so
`core/gateway/namespaces.yaml` makes `envoy-gateway-system` and `gateway`
first.

```
kubectl apply --server-side --field-manager=cloud-infra -f core/gateway/namespaces.yaml
kubectl apply --server-side --field-manager=cloud-infra -f core/gateway/envoy-gateway.yaml
```

A dry run of the second file before the first reports
`Error from server (NotFound): namespaces "envoy-gateway-system" not found`
twelve times. A dry run never really creates a namespace, so its namespaced
objects have nothing to validate against. Same false error the Flannel apply
produced in Phase 2. Not a fault.

### 🔴 The chart installs the EXPERIMENTAL Gateway API channel

`crds.gatewayAPI.channel` defaults to `experimental`, and the CRDs sit in
Helm's `crds/` directory, where **no value can deselect them** — `crds/` is not
templated. A plain `helm install` of Envoy Gateway therefore contradicts
ADR 0008 by default.

`render.sh` pipes the rendered output through
`core/gateway/strip-gatewayapi-crds.py`, which removes them.

**The first version of that filter was wrong, and the mistake is worth
keeping.** It matched group `gateway.networking.k8s.io` only, which is ten
CRDs. Three more survived:

```
xbackends.gateway.networking.x-k8s.io               channel: experimental
xbackendtrafficpolicies.gateway.networking.x-k8s.io channel: experimental
xmeshes.gateway.networking.x-k8s.io                 channel: experimental
```

The experimental channel puts its extension kinds in
**`gateway.networking.x-k8s.io`**, and the upstream safe-upgrade admission
policy does not match that group either. So neither the documented guard nor
the obvious filter would have stopped them. The filter now matches both
groups, and it fails if it strips nothing.

### 🔴 `helm template` corrupts an oci:// render on a cold cache

The first render put these two lines inside the manifest:

```
Pulled: docker.io/envoyproxy/gateway-helm:v1.9.1
Digest: sha256:91bae9aedb91ab34731e987afe01a3ccf454393015abeca705eea8ee15553e86
```

`helm template` prints them on **stdout** when it fetches an `oci://` chart for
the first time. With a warm cache it prints nothing, so the corruption is
intermittent and a second render appears to fix it. `render.sh` now pulls the
chart to a temporary directory first and renders from there, and records the
digest in the file header. The strip filter also fails loudly on any document
that is not a Kubernetes object.

### Verified

```
kubectl -n envoy-gateway-system get pods
envoy-gateway-5f8c9f5b6c-lgm94   1/1   Running
eg-gateway-helm-certgen-7pmp2    0/1   Completed
```

No experimental CRD and nothing in `gateway.networking.x-k8s.io` is present in
the cluster. Controller name, from the rendered ConfigMap:
`gateway.envoyproxy.io/gatewayclass-controller`.

---

## 3. cert-manager, and a wildcard that issues before the load balancer exists

```
kubectl apply --server-side --field-manager=cloud-infra -f core/cert-manager/namespace.yaml
kubectl apply --server-side --field-manager=cloud-infra -f core/cert-manager/cert-manager.yaml
```

cert-manager **v1.21.2**, rendered by `core/cert-manager/render.sh`.

### 🔴 Two chart options that the checklist names wrongly

- `crds.enabled` defaults to **false**. Without it the controller starts and
  then fails on every Certificate, because the types do not exist.
- The Gateway API option is **`config.gatewayAPI.enabled`**. ADR 0008 and the
  Phase 4 checklist both call it `config.enableGatewayAPI`, which this chart
  does not have. The `ExperimentalGatewayAPISupport` feature gate has been on
  by default since 1.15, so the gate was never what was missing.

Proven from the controller's own log, not from the values file:

```
"enabling the sig-network Gateway API certificate-shim and HTTP-01 solver"
enabled controllers: [... clusterissuers gateway-shim ingress-shim orders]
```

### The Cloudflare token

```
kubectl create secret generic cloudflare-dns-token \
  --namespace cert-manager \
  --from-file=api-token=<file holding the token>
```

`--from-file` keeps the value off the command line. The file holds 53 bytes
and **no trailing newline** — a trailing newline is a common cause of a
DNS-01 failure that reads as an authentication error.

The Secret is in `cert-manager` and not in `gateway`, because a ClusterIssuer
resolves secret references in the cert-manager namespace and not in the
namespace of the Certificate that uses it.

The token's scope was checked before use. `GET /user/tokens/verify` returns
`Invalid API Token` and `GET /zones` returns exactly one zone, `tosak.net`.
That pair is the expected signature of a token with `Zone:DNS:Edit` and no
`User` permission — the verify endpoint needs a permission the token correctly
does not have. Zone settings are denied too, which means **this token cannot
enable Authenticated Origin Pulls**; that step needs a second token or the
dashboard.

### ClusterIssuer and certificate

```
kubectl apply --server-side --field-manager=cloud-infra -f core/cert-manager/clusterissuer.yaml
kubectl apply --server-side --field-manager=cloud-infra -f core/gateway/certificate.yaml
```

`letsencrypt-prod` moved from HTTP-01 to **DNS-01 via Cloudflare**. Ready
within seconds: `ACMEAccountRegistered`.

The certificate took about two and a half minutes. Two challenges ran, one per
name, serialised — the second sat at
`Waiting for DNS-01 challenge propagation` while the first was already
`valid`. Issued:

```
subject = CN = *.tosak.net
issuer  = C = US, O = Let's Encrypt, CN = YE1
SAN     = DNS:*.tosak.net, DNS:tosak.net
notAfter= Dec 19 21:33:18 2026 GMT
```

**It issued with no load balancer in existence.** That is the whole reason
ADR 0008 chose DNS-01. The ACME TXT records were cleaned up afterwards: the
zone holds zero TXT records.

### 🔴 The apex is in the certificate, and ADR 0008 said it would not be

ADR 0008 records that "no host uses the apex". The zone holds a **proxied A
record for `tosak.net`**, and a wildcard does not match an apex. Without the
second name, `https://tosak.net` would reach the entry point with no matching
certificate. `tosak.net` is listed beside `*.tosak.net` for that reason.

---

## 4. The Public Entry Point

### GatewayClass and EnvoyProxy first — neither makes a Service

```
kubectl apply --server-side --field-manager=cloud-infra \
  -f core/gateway/envoyproxy.yaml -f core/gateway/gatewayclass.yaml
```

`GatewayClass tosak` reports `Accepted = True | Valid GatewayClass`.

The `EnvoyProxy` carries the Hetzner annotations that used to live on the
hand-written Service in `core/load-balancer/`. Only one was dropped: the mqtt
port 1883, because wasteio is out of scope (ADR 0003).

### 🔴 The old load balancer had to be deleted FIRST

`6579148` held **private IP `10.0.4.2`**, which is the address the `EnvoyProxy`
pins. Hetzner will not give one private address to two load balancers, so the
delete is a prerequisite of the Gateway, not the cleanup step the Phase 4
checklist makes it.

```
curl -X DELETE .../v1/load_balancers/6579148      -> HTTP 204
curl .../v1/load_balancers                        -> 0 load balancers
```

The count was read back from the API, never from the delete's own output.

### Gateway, ClientTrafficPolicy and the redirect, applied together

```
kubectl apply --server-side --field-manager=cloud-infra \
  -f core/gateway/gateway.yaml \
  -f core/gateway/client-traffic-policy.yaml \
  -f core/gateway/http-redirect.yaml
```

One command on purpose. `uses-proxyprotocol` on the `EnvoyProxy` and
`proxyProtocol` on the `ClientTrafficPolicy` are two halves of one setting, and
a listener that disagrees with the load balancer in front of it breaks every
connection.

### 🔴 `enableProxyProtocol` is deprecated

ADR 0008 and the Phase 4 checklist both name `enableProxyProtocol`. The field
still exists, but `kubectl explain` says:

```
Deprecated: Use ProxyProtocol instead.
... If both EnableProxyProtocol and ProxyProtocol are set, ProxyProtocol takes precedence.
```

The policy uses `proxyProtocol: {optional: false}`. `optional: false` means the
PROXY header is **required**: a connection that reaches the Envoy Service
without passing the Hetzner load balancer is refused.

### Gateway API has no automatic HTTP-to-HTTPS redirect

ingress-nginx redirected by default. Gateway API does not: an HTTP listener
serves plain HTTP until a route says otherwise. `core/gateway/http-redirect.yaml`
is that route, and it pins `sectionName: http` — without it the route attaches
to both listeners and the HTTPS listener redirects to itself for ever.

### The new load balancer, read back from the Hetzner API

```
id 7907558 | name tosak-lb | type lb11 | loc hel1
  public v4: 77.42.14.48   v6: 2a01:4f9:c01e:39a::1
  private  : [(11736362, '10.0.4.2')]
  algorithm: round_robin
  service tcp 80  -> 32515  proxyprotocol=True  hc=tcp:32515
  service tcp 443 -> 30784  proxyprotocol=True  hc=tcp:30784
  targets  : 166652124 healthy, 166652125 healthy, 166652126 unhealthy (all use_private_ip=True)
```

**Hetzner reissued the same public address, `77.42.14.48`.** The old load
balancer had just released it. This was luck, not design — but it means the
four A-record updates the checklist calls for were not needed. Do not plan on
it next time.

**One target is `unhealthy` by design.** `externalTrafficPolicy` is `Local`
and there are two Envoy replicas, on `k8swk2` and `k8swk3`. `k8swk1`
(`166652126`) runs no Envoy pod, so its node-port health check fails and the
load balancer sends it nothing. That is the point of `Local`: it keeps the
path direct. A third replica would make all three healthy.

### Acceptance test

A `whoami` Deployment, Service and HTTPRoute were applied to `gateway`,
tested, and deleted. They are **not** committed — ADR 0008 deletes the old
`core/whoami-test-ingress.yaml` for the same reason.

**Straight to the origin, bypassing Cloudflare**, which proves the
PROXY-protocol pair and the certificate:

```
curl --resolve gw-test.tosak.net:443:77.42.14.48 https://gw-test.tosak.net/
HTTP/2 200
Host: gw-test.tosak.net
X-Forwarded-Proto: https
```

**Through Cloudflare**, the real path, using `doma.tosak.net`, which already
resolved correctly:

```
HTTP/2 200      server: cloudflare      cf-ray: a3e453d5de70b0ce-SKP
Cf-Connecting-Ip: 185.100.244.43
X-Forwarded-For:  185.100.244.43
```

`185.100.244.43` is the workstation's public address. So
`clientIPDetection.customHeader` works: Envoy reads `CF-Connecting-IP` and
writes the **visitor's** address into `X-Forwarded-For`, not the Cloudflare
edge address that the PROXY header carried.

**HTTP listener:**

```
curl --resolve gw-test.tosak.net:80:77.42.14.48 http://gw-test.tosak.net/
HTTP/1.1 301 Moved Permanently
location: https://gw-test.tosak.net/
```

Note what the first test also proves: **the origin answers direct connections
today.** Authenticated Origin Pulls is what closes that, and it is not
configured yet. Until it is, `CF-Connecting-IP` is forgeable by anyone who
finds `77.42.14.48`.

---

## 5. The Cloudflare zone

The checklist says "update the four Cloudflare A records". The zone held
**twelve**:

| Record | Proxied | In scope? |
|---|---|---|
| `argocd`, `authos`, `authos-api`, `authos-demo`, `doma` | yes | yes (ADR 0003) |
| `imaps`, `imaps-api`, `wasteio`, `wasteio-api` | yes | no — apps inactive |
| `tosak.net` (apex), `www` | yes | not in any plan document |
| `mqtt` | **no** | dropped with port 1883 |

Because the new load balancer kept `77.42.14.48`, eleven of them were already
correct. Only one change was made:

```
DELETE .../zones/<zone>/dns_records/<mqtt record id>    -> success
```

`mqtt.tosak.net` pointed straight at the origin, unproxied, for a service that
ADR 0003 removes.

Read back from the Cloudflare API: 11 A records, every one proxied, every one
on `77.42.14.48`.

### The hazard this avoided by luck

Deleting a Hetzner load balancer releases its public IP to the pool. Had the
new one been given a different address, all twelve records would have pointed
at an address Hetzner could hand to another customer — and eleven of them are
**proxied**, so Cloudflare would have kept forwarding traffic to a stranger's
server. ADR 0008 calls the rebuild "low risk … an origin change behind the
proxy". That is true for visitors and not true for the origin address itself.

---

## 6. The old shape comes out, and routes go in

Deleted whole, per ADR 0008:

```
core/ingress-controller/      both classes
core/load-balancer/           the hand-written Service
core/longhorn/                ADR 0005
core/whoami-test-ingress.yaml a test
core/argocd/ingress.yaml      and the five projects/*/ingress.yaml
```

Nine `HTTPRoute` objects replace eight `Ingress` objects. Every public host is
covered.

| File | Namespace | Hosts |
|---|---|---|
| `core/argocd/httproute.yaml` | `argocd` | `argocd` |
| `projects/authos/httproute.yaml` | `authos` | `authos`, `authos-api` |
| `projects/authos/demo/manifests/httproute.yaml` | `authos` | `authos-demo` |
| `projects/doma/httproute.yaml` | `doma` | `doma` |
| `projects/imaps/httproute.yaml` | `imaps` | `imaps`, `imaps-api` — **not applied** |
| `projects/wasteio/httproute.yaml` | `wasteio` | `wasteio`, `wasteio-api` — **not applied** |

**None of them is applied yet.** The `argocd`, `authos` and `doma` namespaces
do not exist — ArgoCD is Phase 4's later half and the applications are Phase 5.
The Phase 4 checklist says to "apply routes for argocd, authos, authos-api,
authos-demo and doma"; only the ArgoCD route can be applied in this phase, and
only after ArgoCD is installed.

`imaps` and `wasteio` are committed and unapplied, matching how the rest of
their manifests sit in the repository (ADR 0003). Two things keep that honest:
their namespaces do not exist, and the Gateway's `allowedRoutes` does not name
them, so an accidental apply is refused with `NotAllowedByListeners` rather
than quietly claiming a host.

### No `URLRewrite` filters, as ADR 0008 requires

The three `rewrite-target: /` annotations were inert and are not carried.
`/duster` on `authos-demo.tosak.net` keeps its path, which under Gateway API is
the default rather than something to configure. Rule order is irrelevant there:
Gateway API ranks matches by specificity, so `/duster` beats `/` however they
are written.

### 🔴 ADR 0008 missed doma's SSE annotations

The ADR audited `rewrite-target` across every Ingress and concluded the
annotations were inert. It did not look at the other annotations.
`projects/doma/ingress.yaml` carried two:

```
nginx.ingress.kubernetes.io/proxy-buffering: 'off'
nginx.ingress.kubernetes.io/proxy-read-timeout: '3600'
```

The file's own comment says they exist for M7, server-sent events. They are
**not** inert, and each needs a different answer:

- `proxy-buffering: off` needs no equivalent. Envoy streams responses; it does
  not buffer them the way nginx does by default.
- `proxy-read-timeout` **does**. **Envoy's default route timeout is 15
  seconds**, so without an equivalent an SSE stream is cut after 15 seconds —
  a regression that would have shipped silently and looked like an application
  bug.

A literal translation would also have been wrong. nginx's `proxy_read_timeout`
is an **inactivity** timeout between reads; Gateway API's `timeouts.request` is
the **total** duration of the request. Copying `3600` across would cap a
healthy stream at one hour, which the nginx setting never did.
`timeouts.request: 0s` disables the timeout and preserves the intent.

**The lesson repeats one the ADR itself recorded:** check the claim against the
code. The ADR checked one annotation carefully and generalised from it.

---

## 7. Documentation that taught the old shape

ADR 0008 flags `README.md` and `~/Projects/Active/CLAUDE.md` for describing two
ingress-nginx classes. Reading them found more than that.

**`README.md`** — the ingress section was rewritten around the single Public
Entry Point, and three other statements were corrected:

- The storage bullet still named Longhorn.
- The automation-scripts paragraph still described `reset-nodes.sh`, deleted in
  Phase 3, as a working tool.
- 🔴 **The "Issues with ephemeral nodes" section still recommended the
  timestamped node names** — presenting as a fix the exact mechanism that
  destroyed the cluster on 2026-09-13. It now says so, and says that removing
  Longhorn removed the constraint that forced unique names.

**`CLAUDE.md` in this repository** — the components table, the directory
layout, the new-project checklist and the "Ingress rules" section. The CNPG
examples were also still on the pre-ADR-0007 names, `authos-pg-cluster` and
owner `authos`. Note that this file is **gitignored** (`.gitignore:5`), so the
correction is local to this workstation and is not in the commit. Any other
machine still holds the old text.

**`~/Projects/Active/CLAUDE.md`** — the same, plus its `Ingress` template,
which is what a new project would have been built from. Backed up first to
`CLAUDE.md.bak-phase4-2026-09-21`; it is not in git.

**`core/cni/README.md`** — unrelated to ingress, found while reading
conventions. It still said `Backend.MTU = 1400` with the reasoning "the network
is 1450 and VXLAN costs 50", while the manifest correctly says `1450`. That
reasoning is the double-subtraction bug Phase 2 found and fixed, left standing
in the README where the next reader would have trusted it.

---

## 8. State at the end of the ingress stack

| | |
|---|---|
| Gateway API | v1.6.2, **standard channel only**, safe-upgrade policy active |
| Controller | Envoy Gateway v1.9.1, `envoy-gateway-system` |
| Gateway | `tosak` in `gateway`, `Programmed`, 2 Envoy replicas on `k8swk2` and `k8swk3` |
| Certificate | `*.tosak.net` + `tosak.net`, Let's Encrypt, expires 2026-12-19 |
| Load balancer | Hetzner `7907558` `tosak-lb`, `77.42.14.48`, private `10.0.4.2` |
| PROXY protocol | on at both halves, proven end to end |
| Client address | `CF-Connecting-IP` → `X-Forwarded-For`, proven through Cloudflare |
| Cloudflare zone | 11 A records, all proxied, all on `77.42.14.48` |
| Routes applied | one — the HTTP-to-HTTPS redirect in `gateway` |

**The origin is NOT locked.** Authenticated Origin Pulls is deliberately last
and is not done. `77.42.14.48` answers direct HTTPS today, and `CF-Connecting-IP`
is forgeable by anyone who finds it.

### A mistake made and corrected here

Two commits in this session used `git add -A <dir>`, which swept in two files
that three handoffs had deliberately left untracked: the stale vim swap
`docs/runbook/.rebuild-2026-09-20.md.swp`, and
`projects/imaps/backend/manifests/configmap.yaml`, which the operator had not
reviewed. Both were removed from the index in a follow-up commit and are
untracked again; the swap file is now covered by a `*.swp` rule in
`.gitignore`. They remain in the history of two unpushed commits, which is
harmless — neither holds a secret.

**Use explicit paths with `git add`, not `-A` over a directory.** An untracked
file in this repository is usually untracked on purpose.

### What Phase 4 still owes

Non-ingress work, untouched by this session: CNPG and `tosak-pg-cluster` —
which will be the **first real volume attach in the rebuilt cluster**, and so
the first real test of the CSI driver; Redis; the monitoring repair; ArgoCD
with its route, the gRPC acceptance test and its notifications; and the
ApplicationSet.

---

## 9. The CloudNativePG operator

Written on 2026-09-21, second session of the day. Steps 1 to 8 built the
Public Entry Point; this step starts the data layer.

**Starting state, re-verified before touching anything:** 4 nodes `Ready`
v1.37.0, 27 pods, the only non-`Running` pod a `Completed` cert-manager
startup job. Namespaces `cert-manager`, `envoy-gateway-system`, `gateway`.
`hcloud-volumes (default)`, **`WaitForFirstConsumer`, and not one
PersistentVolume in the cluster.**

**v1.30.0**, the newest stable, published 2026-06-29.

### Why this one is not rendered from a chart

`core/gateway` and `core/cert-manager` each carry a `render.sh` because each
needs values set. **The CloudNativePG operator needs none** — it watches every
namespace by default and its image is already pinned inside the released
manifest. A chart, a values file and a render script would add three files that
configure nothing.

So `core/cnpg/operator.yaml` follows this repository's other pattern, the one
`core/gateway-api/crds.yaml` and `core/cni/flannel.yaml` use: a named upstream
release, copied unmodified. It was diffed against the release URL after copying
and is byte for byte identical.

```
sha256  f8bede43fe4ee0d478c2355b204a36876b2ae4faac60f2a9452280b293da3b88
```

### Applied

```
kubectl apply --server-side --field-manager=cloud-infra -f core/cnpg/operator.yaml
kubectl apply -f core/cnpg/namespace.yaml
```

`--server-side` for the same reason as the Gateway API CRDs: the
`clusters.postgresql.cnpg.io` CRD alone is about 7,700 lines, and a
client-side apply writes the whole object into the
`last-applied-configuration` annotation, overflowing the 256 KiB annotation
limit.

`namespace.yaml` declares `pg-cluster`, which holds the cluster and its
Secret. It is **not** the operator's namespace — `operator.yaml` declares
`cnpg-system` itself.

### Verified

| | |
|---|---|
| Operator pod | `cnpg-controller-manager-66b5b6b645-7xgnj` `1/1 Running` on `k8swk2` |
| Image running | `ghcr.io/cloudnative-pg/cloudnative-pg:1.30.0` |
| CRDs | 11, every one `Established=True` |
| Webhook certificate | `cnpg-webhook-cert` present, `caBundle` 956 bytes on all 9 webhooks |
| Operator log | no `"level":"error"` and no panic |

The webhook `caBundle` is worth checking rather than assuming. CloudNativePG
issues its own webhook certificate at startup and patches it into both webhook
configurations. Creating a `Cluster` before that lands fails at admission, and
the message points at TLS rather than at timing.

### 🔴 The PostgreSQL image was not pinned, and now is

`core/cnpg/pg-cluster.yaml` carried no `imageName`. That leaves the
**PostgreSQL major version** to whatever the operator defaults to on the day.
The default was read out of the source rather than guessed:

```
pkg/versions/versions.go
  DefaultImageName = "ghcr.io/cloudnative-pg/postgresql:18.6-system-trixie"
```

That tag was confirmed to exist in `ghcr.io` (HTTP 200 on its manifest) and is
now written into `pg-cluster.yaml` explicitly, with the reasoning in a comment.

The failure this prevents is not a corrupted upgrade — CloudNativePG refuses a
major-version change on a running cluster. It is a **rebuild**: this repository
exists so the cluster can be rebuilt from it, and an unpinned image means a
rebuild in a year comes up on a different PostgreSQL major than the one that
was tested. Every other version in this repository is pinned; this was the
exception.

### 🔴 Nothing in the repository created `db-credentials`

`pg-cluster.yaml` names `db-credentials` at
`spec.bootstrap.initdb.secret.name`, and **no file, script or runbook in this
repository created it.** It was made by hand on the dead cluster and never
recorded. The same is true of the `redis` Secret, which
`core/redis/deployment.yaml` reads through a `secretKeyRef`.

Phase 5's inventory lists both, so neither is forgotten — but a Phase 4 step
that applies `pg-cluster.yaml` would have stalled at bootstrap with no
explanation in this repository. `core/cnpg/credentials.yaml` now documents the
shape, as `core/cert-manager/credentials.yaml` does for the Cloudflare token.

**Three properties of that Secret are load-bearing, each read out of the
CloudNativePG v1.30.0 source rather than assumed:**

1. **`username` must equal `initdb.owner`**, so `tosak`. The instance manager
   compares them and fails with `wrong username '<x>' in secret, expected
   '<y>'` (`internal/management/controller/instance_controller.go`,
   `reconcileUser`).
2. **Both `username` and `password` must exist.** The initdb Job mounts
   `username` with `Optional: false` (`pkg/specs/jobs.go`), so a Secret missing
   it stops the bootstrap before PostgreSQL starts.
3. 🔴 **The `cnpg.io/reload=true` label is what makes a password ROTATION
   work**, and its absence is silent. The operator reconciles on a Secret
   change only when that Secret is owned by a `Cluster` or carries this label
   (`internal/controller/cluster_predicates.go`, `hasReloadLabelSet`).
   CloudNativePG sets it on every Secret it creates itself. A hand-made Secret
   without it **bootstraps perfectly**, because the instance manager reads
   Secrets uncached and directly at that moment — the omission only surfaces on
   the day someone edits the password and PostgreSQL quietly keeps the old one.

The third is the same failure class this rebuild keeps meeting: a default that
points the wrong way, and fails without saying so.

---

## 10. `tosak-pg-cluster` — the first volume attach

This is the step the rebuild had not yet tested. Everything before it ran on
node-local storage; **no PersistentVolume had existed in this cluster since it
was built.** The hcloud CSI driver was installed in Phase 2 and had never been
asked to do anything.

### The Secret first

```
LC_ALL=C tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 40 > <scratch file, mode 600>

kubectl create secret generic db-credentials --namespace pg-cluster \
  --type kubernetes.io/basic-auth \
  --from-literal=username=tosak \
  --from-file=password=<scratch file>

kubectl label secret db-credentials --namespace pg-cluster cnpg.io/reload=true
```

**Alphanumeric on purpose.** The project configmaps build their connection
strings by hand as `postgresql://tosak:<password>@…`, so a `/`, `+`, `=` or
`@` in the password would break them — and break them at the application, far
from here. 40 characters from that alphabet is about 238 bits.

Read back: type `kubernetes.io/basic-auth`, keys `password` and `username`,
label `cnpg.io/reload: "true"`, username `tosak`, password 40 characters and
alphanumeric.

### The cluster

```
kubectl apply -f core/cnpg/pg-cluster.yaml
kubectl wait --for=condition=Ready cluster/tosak-pg-cluster -n pg-cluster --timeout=900s
```

**Ready in 3 minutes 4 seconds.** The first PVC was `Bound` within 20 seconds
of the apply — so the answer to the one open question of this step is that the
CSI driver works, first time, with nothing to fix.

| | |
|---|---|
| Phase | `Cluster in healthy state`, 3 instances, 3 ready |
| Primary | `tosak-pg-cluster-1` on `k8swk3` |
| Replicas | `-2` on `k8swk2`, `-3` on `k8swk1` — one per worker |
| Image | `ghcr.io/cloudnative-pg/postgresql:18.6-system-trixie` |
| Replication | both replicas `streaming`, `async` |
| Services | `-rw` `10.96.22.127`, `-ro` `10.96.228.232`, `-r` `10.96.152.129` |

Anti-affinity placed one instance per worker without being asked to. That is
CloudNativePG's default and it happens to be exactly what three workers want.

### Verified over the Hetzner API, not from kubectl

```
GET /v1/volumes
```

Three volumes, each **10 GB in `hel1`**, each labelled by the CSI driver with
the PVC it backs:

| Volume | PVC | Attached to server | Which worker |
|---|---|---|---|
| `106916161` | `tosak-pg-cluster-1` | `166652124` | `k8swk3` |
| `106916168` | `tosak-pg-cluster-2` | `166652125` | `k8swk2` |
| `106916171` | `tosak-pg-cluster-3` | `166652126` | `k8swk1` |

Each attachment matches where the pod actually runs, checked against the
server IDs recorded in `docs/runbook/workers.md`. 10 GB is also exactly
Hetzner's minimum volume size, so the ADR 0005 figure is the floor, not a
choice that can be trimmed.

🔴 **`protection.delete` is `false` on all three volumes.** The control plane
server and the primary IP both carry delete protection; these do not, and they
hold the only copy of every database. Turning it on is not free — the
`hcloud-volumes` StorageClass reclaims with `Delete`, so a protected volume
would make an ordinary PVC deletion fail rather than tidy up. **This belongs
with the Phase 6 survivability work, next to the backups**, and is recorded
here so it is not discovered later as a surprise.

### The database inventory

```
kubectl apply -f core/cnpg/databases/doma.yaml
```

```
authos  | tosak
doma    | tosak
postgres| postgres
```

`authos` came from `bootstrap.initdb.database` and is not a `Database` object.
`doma` is, and reports `APPLIED true`.

**`databases/imaps.yaml` and `databases/wasteio.yaml` were NOT applied** and
the namespace holds exactly one `Database` object. Those projects are inactive
(ADR 0003); their manifests stay committed and unsynced.

Roles present: `tosak` (owner, login, not superuser), `postgres`,
`streaming_replica`, `cnpg_metrics_exporter`. Superuser access is disabled,
which is CloudNativePG's default since 1.21 and is left alone.

### The Secret was proven, not assumed

```
psql -h tosak-pg-cluster-rw.pg-cluster.svc.cluster.local -U tosak -d authos \
  -c "SELECT current_user, current_database(), inet_server_addr();"

tosak|authos|10.244.3.8
```

`10.244.3.8` is `tosak-pg-cluster-1`, the primary. This proves three separate
things at once that a healthy cluster does not: the password in the Secret is
the password PostgreSQL actually set, the `tosak` role can log in to the
application database, and the `-rw` service routes to the primary.

PostgreSQL reports `18.6 (Debian 18.6-1.pgdg13+2)`, matching the pin exactly.

### 🔴 The password is in this session's transcript

It was generated here and printed once for the operator's password manager,
which puts it in the transcript just as pasting it in would have. **It joins
the Cloudflare DNS token on the list of credentials Phase 5 should rotate when
it moves secrets to SOPS.**

Rotation will work, and that is not automatic: it works because of the
`cnpg.io/reload=true` label set above. Without that label the rotation would
have reported success and changed nothing in PostgreSQL.

### There are still no backups

`tosak-pg-cluster` has no `backup` stanza, which is the precise gap that made
the 2026-09-13 loss unrecoverable. It is Phase 6 work by the checklist's own
sequencing, not an oversight — but **until Phase 6 closes, every database here
is disposable.** Note also that CloudNativePG has moved barman-cloud out of the
operator into a plugin, so Phase 6 is a plugin install rather than a stanza.

## 11. Redis — the shared cache

`core/redis/` held two files, `deployment.yaml` and `service.yaml`, written in
December 2025 and never reviewed since. Reading them before applying anything
found six problems. Three of them would have failed silently, which is the
pattern this whole rebuild keeps meeting.

### What the two existing files got wrong

| Problem | What it would have done |
|---|---|
| No `namespace.yaml` | The Deployment, the Service and the Secret all name `redis` and nothing created it |
| No `credentials.yaml` | The Deployment reads a `redis` Secret through `secretKeyRef` that **nothing in this repository created** — the same gap `db-credentials` had in step 10 |
| `image: redis:8.4-alpine` | A floating minor tag. The patch version was whatever the day gave. Trap from step 9, repeated |
| `--maxmemory 512mb` with `limits.memory: 512Mi` | No headroom at all. Redis fills to its own limit, the kernel then OOM-kills the container |
| No `securityContext` | **Redis ran as root** — see below, this one is not obvious |
| Default `RollingUpdate` | One Service, one replica holding state no other replica has: a rollout would briefly route sessions to a second, empty Redis |

### 🔴 `command:` replaces the entrypoint, and with it the privilege drop

`deployment.yaml` carries `command: ['redis-server']`. In Kubernetes that
replaces the image **ENTRYPOINT**, not its CMD — so `docker-entrypoint.sh`
never runs.

That script is where the official image drops privileges. Read out of the image
rather than assumed:

```
exec $SETPRIV --nnp --inh-caps=-all ... "$0" "$@"
#   SETPRIV="/bin/setpriv --reuid redis --regid redis --clear-groups"
```

It runs that branch only when `id -u` is `0`. Override the entrypoint and the
drop is skipped, so the container keeps the uid it was given — root, unless the
pod says otherwise. The manifest did not say otherwise.

The fix needs the real ids, so they were read from the image's own
`/etc/passwd`, pulled from the registry layer:

```
redis:x:999:1000::/home/redis:/sbin/nologin
```

Hence `runAsNonRoot: true`, `runAsUser: 999`, `runAsGroup: 1000`,
`fsGroup: 1000`, plus `readOnlyRootFilesystem`, dropped capabilities and an
`emptyDir` on `/data`, which is the image's WORKDIR.

The same override has a second consequence: the entrypoint is also what appends
a `--loadmodule` for every `.so` in `/usr/local/lib/redis/modules/`. Skipping it
leaves `redisearch.so` (20 MB), `rejson.so` (45 MB), `redistimeseries.so` and
`redisbloom.so` in the image and unloaded. `MODULE LIST` shows one entry,
`vectorset`, which is built into the Redis 8 binary and is there either way.
That is wanted here, but it means restoring the entrypoint would quietly change
the memory budget.

### The image, pinned and verified

```
redis:8.10.2-alpine
sha256:72cedd9603038893af961e90ac5e1a1a0d8377d5e338dbdad8fe284ea25de18f
```

`8.10` is the current line — `latest`, `alpine` and `8-alpine` all resolve to
the same digest. `8.4` is three minor lines behind and still rebuilt, so the
old tag would have gone on looking maintained. The digest above was taken from
the registry before the apply and matched against
`.status.containerStatuses[0].imageID` after it.

### Apply

The Secret must exist before the Deployment, or the pod stops at
`CreateContainerConfigError` with the cause named nowhere.

```
kubectl apply -f core/redis/namespace.yaml
# operator creates the `redis` Secret — see core/redis/credentials.yaml
kubectl apply -f core/redis/service.yaml -f core/redis/deployment.yaml
kubectl rollout status deployment/redis -n redis
```

### 🔴 The Secret was stored with a trailing newline, and the pod went Ready anyway

The first `redis` Secret held **41 bytes**: 40 alphanumerics and `0a`.
`kubectl create secret --from-file` stores a file byte for byte.

It was invisible from outside the pod. The obvious check is the base64 length:

```
kubectl get secret redis -n redis -o go-template='{{len .data.password}}'   # 56
```

**40 and 41 bytes both encode to 56 characters**, so that number proved
nothing, and it was quoted here as if it had. The real check prints no value
and runs inside the pod:

```
kubectl exec -n redis deploy/redis -- \
  sh -c 'printf "%s" "$REDIS_PASSWORD" | wc -c'                        # 40
kubectl exec -n redis deploy/redis -- \
  sh -c 'printf "%s" "$REDIS_PASSWORD" | tr -d "A-Za-z0-9" | wc -c'    # 0
```

Redis was perfectly happy: `--requirepass "$(REDIS_PASSWORD)"` and the
readiness probe both quote the variable, so the 41-byte password was set and
accepted and the pod reported `1/1 Ready`. The fault surfaced only because an
ad-hoc check used the variable **unquoted**, which strips the trailing newline
and returned `WRONGPASS`.

**The damage would have landed a phase later, in another namespace.** Authos
and Duster read this password from `credentials`/`REDIS_PASS` in the `authos`
namespace. A human copying "the password" copies the 40 characters they can
see, writes those into the second Secret, and both applications fail with
`NOAUTH` against a Redis that is running and healthy.

Corrected by stripping the newline and keeping the same 40 characters, so the
copy already in the password manager became the correct one:

```
kubectl get secret redis -n redis -o jsonpath='{.data.password}' \
  | base64 -d | tr -d '\n' > <scratch file, mode 600>
kubectl create secret generic redis -n redis --from-file=password=<scratch file> \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl rollout restart deployment/redis -n redis
```

🔴 **The classifier refuses that first command to Claude**, as
`Credential Materialization` — it will not read a Secret's value out of the
cluster into a file, even down a pipe that prints nothing. The operator ran it.
This is a new refusal category; it is not in any earlier handoff.

### Verified after the restart

```
kubectl exec -n redis deploy/redis -- sh -c 'redis-cli --no-auth-warning -a $REDIS_PASSWORD ping'
```

`PONG` — the unquoted form, the exact test that had failed.

| | |
|---|---|
| Pod | `redis-848765885d-pxpq7` on `k8swk3`, `10.244.3.10`, `1/1 Running` |
| Image | `redis@sha256:72cedd96…`, matching the pin |
| Identity | `uid=999(redis) gid=1000(redis)` — not root |
| Filesystem | `/` read-only (`touch /nope` → `Read-only file system`), `/data` writable |
| Version | `redis_version:8.10.2`, `redis_mode:standalone` |
| Memory | `maxmemory_human:384.00M`, policy `volatile-lru`, used 833 K, RSS 9.88 M |
| Persistence | `save` empty, `aof_enabled:0` — nothing is written |
| Modules | one, `vectorset`, built into the binary |
| Service | `redis-master` ClusterIP `10.96.30.54:6379`, one endpoint, `ready: true` |
| Round trip | `SET`/`GET`/`DEL` through an authenticated client |

### The password is the only thing keeping other pods out

From a PostgreSQL pod in `pg-cluster`, a namespace with no business talking to
Redis:

```
getent hosts redis-master.redis.svc.cluster.local     # 10.96.30.54
timeout 3 bash -c "</dev/tcp/redis-master.redis.svc.cluster.local/6379"   # opens
```

Flannel enforces no NetworkPolicy (ADR 0001), so every pod in this cluster can
open that port. `--requirepass` is the entire boundary. This is the standing
argument for Cilium; until then the Redis password is a cluster-wide
credential, and it belongs in the Phase 5 SOPS inventory as one.

One mitigation that does hold: Redis rewrites its own `argv`, so
`/proc/1/cmdline` in the pod contains a single entry, `redis-server *:6379`.
The `--requirepass` argument is scrubbed and cannot be read back out of the
process list.

### Nothing here survives a restart, on purpose

`--save ''` and `--appendonly no`, no volume, no backup. **Any restart signs
every user out.** That is acceptable for a cache and a session store and is
why the memory headroom can be as small as it is — `redis-server` never forks
to write a snapshot. It would not be acceptable for anything else, so nothing
else goes here.

## 12. ArgoCD — and the gRPC claim ADR 0008 could not settle

`core/argocd/` held two things before this step: `httproute.yaml`, and a
`notifications/` directory with a tracked ConfigMap. **Nothing in this
repository had ever installed ArgoCD.** The old install was done out of band
and never written down, so there was nothing to recover and nothing to
correct — only the route, which was wrong, and the notification config, which
was in the wrong place.

ArgoCD **v3.5.3**, chart **10.9.2**, digest
`sha256:8a82bf6d8ac9f6f126a1eb9d0c5a68ce2db64f8f66a4da5c00b32cc3ef3fc93f`.
The current line, pinned, the same choice step 11 made for Redis.

### Why this is a render and not an upstream manifest

The rule from step 9: render from a chart only where values are actually set.
ADR 0008 asks two things of ArgoCD, and both are plain chart values, so a
render keeps them out of a generated file where a hand edit dies at the next
upgrade — `--insecure` on `argocd-server`, and a Service port advertising
`appProtocol: kubernetes.io/h2c`.

The render is deterministic, which was checked rather than assumed: two runs
of `render.sh` produce identical files, and no template in the chart calls
`randAlphaNum`, `genCA` or `uuidv4`.

🔴 **One value would break that.** Setting
`configs.secret.argocdServerAdminPassword` renders `admin.passwordMtime` from
`now`, so the manifest would differ on every render and the commit diff would
be worthless. It is not set. The initial password comes from
`argocd-initial-admin-secret`, which `argocd-server` generates itself.

### 🔴 `appProtocol` is single-valued per Service port, and ADR 0008 needs it not to be

ADR 0008 states that "the `argocd-server` Service port carries `appProtocol:
kubernetes.io/h2c`", and the committed `httproute.yaml` sent everything to
port 80. Both are wrong, and the first one is wrong in a way that takes the
web UI down completely.

`argocd-server` serves the web UI and gRPC on **one** container port and
splits them with cmux. In the insecure branch the two matchers are exclusive
(`server/server.go`, v3.5.3, lines 633-636):

```go
httpL = tcpm.Match(cmux.HTTP1Fast("PATCH"))
grpcL = tcpm.MatchWithWriters(cmux.HTTP2MatchHeaderFieldSendSettings(
    "content-type", "application/grpc"))
```

There is no `cmux.Any()` fallback in that branch. So the UI listener accepts
**HTTP/1.x only**, and an h2c connection that is not gRPC matches nothing and
is dropped. Nor can the chart express the ADR's wording: there is no
`servicePortHttpAppProtocol` value, and `templates/argocd-server/service.yaml`
can set `appProtocol` on the `http2` and `https` ports only.

The shape that works is two ports and two route rules. ArgoCD's own ingress
documentation describes the same split.

| Service port | Name | `appProtocol` | Upstream protocol | Routed |
|---|---|---|---|---|
| 80 | `http` | none | HTTP/1.1 | yes — UI, REST, gRPC-web |
| 8080 | `http2` | `kubernetes.io/h2c` | HTTP/2 cleartext | yes — gRPC |
| 443 | `https` | none | — | no |

All three target container port 8080. Port 443 serves cleartext while
`server.insecure` is true; the chart always renders it and no value removes
it. Nothing points at it.

Rule order in the HTTPRoute does not decide the split — Gateway API precedence
does. Both rules carry the same `/` prefix, so the tie breaks on the number of
header matches, and the gRPC rule has one. Confirmed in Envoy's own config:

```
route httproute/argocd/argocd/rule/0/... -> cluster .../rule/0
   match: {"prefix": "/", "headers": [{"name": "Content-Type", ...}]}
route httproute/argocd/argocd/rule/1/... -> cluster .../rule/1
   match: {"prefix": "/"}

cluster .../rule/0  explicit_http_config: {http2_protocol_options: {...}}
cluster .../rule/1  (no protocol options — HTTP/1.1)
```

### Other values, and why

**Dex is disabled.** No SSO is configured. Dex with no connector is a
Deployment, a Service, two Secrets and a ServiceAccount that do nothing.

**ArgoCD keeps its own Redis.** The shared instance from step 11 is a 384 MB
session store on `volatile-lru` read by Authos and Duster. ArgoCD's cache is
churn; sharing would make the two compete for the same memory, and an ArgoCD
eviction would surface as a logged-out user in another namespace.

🔴 **That bundled Redis needed the step 11 lesson applied.** The chart runs it
with `--save '' --appendonly no` and **no `--maxmemory` at all**, so a memory
limit on its own is an OOM kill waiting for the cache to fill. It runs with
`--maxmemory 192mb` against a 256Mi limit. `allkeys-lru` is right here and
wrong for the shared instance: everything in this one is a cache ArgoCD can
rebuild, so evicting a key with no TTL is correct.

**Resources are set on all six components**, against a chart default of none.
600m CPU and 1216Mi memory requested in total. ArgoCD is the control plane for
every deploy and a BestEffort pod is evicted first; two workers already
requested about half their memory, and an unbounded application-controller
could push a node into memory pressure and take CloudNativePG or Redis with
it. Neither has a backup yet.

**The notification config moved into the values file.** It used to be a tracked
ConfigMap applied over the one the chart renders, so the rendered manifest held
data that was overwritten immediately and re-applying the render reverted the
configuration. `core/argocd/notifications/` is deleted; the `oncePer`
rationale lives in `argocd-values.yaml` beside the trigger.

**`notifications.secret.create: false`.** The chart otherwise renders an empty
Secret with a `stringData:` key and nothing under it, which is a way to lose
the token a later patch put there. `core/argocd/credentials.yaml` documents
the shape — the same gap `db-credentials` and the `redis` Secret both had.

**The five NetworkPolicy objects are kept, and they are inert.** The cluster
runs Flannel, which does not enforce NetworkPolicy. They cost nothing and
become correct the day a CNI that enforces them arrives, which ADR 0001 names
as the trigger for Cilium. Do not read them as a live control.

### Apply

```bash
kubectl apply -f core/argocd/namespace.yaml
kubectl apply --server-side -f core/argocd/argocd.yaml

# The operator creates this one — core/argocd/credentials.yaml has the shape.
kubectl create secret generic argocd-notifications-secret \
  --namespace argocd --from-literal=github-token=<FINE_GRAINED_PAT>

kubectl apply -f core/argocd/httproute.yaml
```

🔴 **`--server-side` is mandatory.** The `applicationsets.argoproj.io` CRD
alone renders to about 23,000 lines, far over the 256 KiB limit on the
`last-applied-configuration` annotation a client-side apply writes. The same
constraint the CloudNativePG operator has in step 9.

### Verified

Six workloads, one Job, spread over all three workers:

| Pod | Node |
|---|---|
| `argocd-application-controller-0` (StatefulSet) | `k8swk3` |
| `argocd-repo-server` | `k8swk3` |
| `argocd-server` | `k8swk2` |
| `argocd-applicationset-controller` | `k8swk2` |
| `argocd-notifications-controller` | `k8swk1` |
| `argocd-redis` | `k8swk1` |

`argocd-server` ClusterIP `10.96.226.141`. Cluster total **37 pods Running**,
1 Completed. The Service ports came out as designed, and only one carries the
protocol hint:

```
port 80    name http   appProtocol (none)
port 8080  name http2  appProtocol kubernetes.io/h2c
port 443   name https  appProtocol (none)
```

`argocd-server` logs `tls: false` and `url: https://argocd.tosak.net`, so
insecure mode and the domain both took. The HTTPRoute is `Accepted` with
`ResolvedRefs` true.

The UI answers 200 over both HTTP/1.1 and HTTP/2 from the client side, and
serves the real ArgoCD document. Note that Cloudflare re-originates to the
Gateway over HTTP/2 whatever the client used, so every request arrives at
Envoy as HTTP/2 — the client-side protocol is not what the backend sees.

Two things behave better than the old notes claimed:

- **The notifications controller does not crash-loop with its Secret absent.**
  It starts, logs `Controller is running.` as a warning, and waits.
- **It watches the Secret.** It logged `invalidated cache for resource … with
  the name: argocd-notifications-secret` at the moment the Secret was created,
  with no restart. The old `notifications/README.md` told the operator to
  `rollout restart` the controller; that is not needed.

The `argocd-redis-secret-init` Job carries `ttlSecondsAfterFinished: 60`, so
it deletes itself a minute after completing. Re-applying the manifest
recreates it, which is harmless — it only writes keys that are missing.

The `github-token` in `argocd-notifications-secret` decodes to **93 bytes**,
which is exactly `github_pat_` plus 82 characters. A trailing newline would
make 94, so that length is itself the proof the step 11 trap was avoided —
and it was read without the value reaching anything.

### 🔴 The acceptance test I ran first was worthless, and it looked like a pass

`argocd version --server argocd.tosak.net` printed `argocd-server: v3.5.3`,
which appears to prove plain gRPC end to end. It proves nothing. The CLI
skips its gRPC probe entirely when the local config already says so
(`pkg/apiclient/apiclient.go`, `if !c.GRPCWeb { … }`), and
`~/.config/argocd/config` on this workstation carried:

```yaml
servers:
  - {grpc-web: true, grpc-web-root-path: '', server: argocd.tosak.net}
```

left over from the ingress-nginx cluster — the very history ADR 0008 cites as
the reason ArgoCD prefers passthrough. So the CLI sent gRPC-web, printed no
warning, and the Envoy access log showed the request served by **rule/1**, the
HTTP/1.1 fallthrough. One request per invocation, so there was no failed first
attempt to notice either.

Forcing a real attempt with `--config <an empty path>` gave the truth:

```
$ argocd version --server argocd.tosak.net --config /tmp/clean.conf --short
argocd: v3.1.11+cc053b2
{"level":"warning","msg":"Failed to invoke grpc call. Use flag --grpc-web ..."}
argocd-server: v3.5.3
```

**Plain gRPC failed, and nothing reached Envoy at all** — so the failure was
upstream of the origin.

*The lesson is step 11's, in a new place: vary the shape of a check. The
quoted, convenient form of this test agreed with the design and hid the
failure. A second reading of the same shape would have re-proved nothing.*

### The origin is correct. Cloudflare blocks plain gRPC

Isolated with a hand-built gRPC request — a unary call to `VersionService` is
an empty five-byte frame, so it needs no tooling and no credentials:

```bash
printf '\x00\x00\x00\x00\x00' > /tmp/grpc-empty.bin

# direct to the load balancer, Cloudflare bypassed
curl --http2 --resolve argocd.tosak.net:443:77.42.14.48 \
  -H 'content-type: application/grpc' -H 'te: trailers' \
  --data-binary @/tmp/grpc-empty.bin \
  https://argocd.tosak.net/version.VersionService/Version
```

| `content-type` | through Cloudflare | direct to the load balancer |
|---|---|---|
| `application/grpc` | **403** | **200**, `grpc-status: 0`, body `v3.5.3` |
| `application/grpc+proto` | **403** | **200**, `grpc-status: 0`, body `v3.5.3` |
| `application/grpc-web+proto` | 200 | 200 |
| `application/octet-stream` | 404 — from Envoy, so it passed the edge | — |

**ADR 0008's unproven claim is PROVEN at the origin.** Envoy terminated TLS,
matched `rule/0`, spoke h2c to `10.244.2.14:8080` and argocd-server's gRPC
listener answered with `grpc-status: 0`. Terminating at the Gateway does carry
the CLI's gRPC. TLS passthrough and a `TLSRoute` are not needed.

**The 403 is Cloudflare's, and it is content-type specific** — `grpc-web` and
`octet-stream` both pass, so it is the zone's gRPC setting being off and not a
WAF rule. A `Zone:DNS:Edit` token cannot change it; that needs the dashboard or
a second token, the same limit the origin lock has.

This costs nothing today. The CLI probes plain gRPC, fails, and retries over
gRPC-web by itself, which the second rule serves. Rule 0 stays: it is proven,
and it is the path the moment the zone setting is enabled or Cloudflare is
bypassed.

### 🔴 The header match the test corrected

The first version matched `Content-Type` exactly against `application/grpc`.
The table above shows why that is too narrow: a gRPC client that sets a
content subtype sends `application/grpc+proto`, which would have fallen
through to the HTTP/1.1 rule and broken. ArgoCD's documentation has the
opposite fault — its `^application/grpc.*$` also matches
`application/grpc-web+proto`, and cmux's gRPC matcher accepts the bare value
only, so gRPC-web would be sent to a listener that cannot match it.

The route uses `^application/grpc(\+.*)?$`, which takes both gRPC forms and
leaves gRPC-web to fall through. Verified at the origin after re-applying:
`application/grpc` and `application/grpc+proto` both served by `rule/0` with
`grpc-status: 0`, `application/grpc-web+proto` by `rule/1`.

### The acceptance test, completed

The operator ran `argocd login`. `argocd account get-user-info` returns
`Logged In: true, Username: admin`, and `argocd app list` returns its header
row with no applications — which is the correct answer, because none exists
yet. `argocd-server` logged the call:

```
grpc.code=OK grpc.service=application.ApplicationService grpc.method=List
  grpc.method_type=unary protocol=grpc
```

and Envoy served it on `rule/1`, because Cloudflare blocks plain gRPC and the
CLI fell back to gRPC-web by itself. **ADR 0008's acceptance test passes.**

One detail worth knowing: a fresh `argocd login` writes the server into the
local config **without** `grpc-web`, so from then on every CLI call probes
plain gRPC, loses that round trip at the Cloudflare edge, and prints
`Failed to invoke grpc call. Use flag --grpc-web`. It is cosmetic. Either
enable the zone's gRPC setting, or log in with `--grpc-web` so the config
records it and the probe is skipped.

### What this step leaves open

- **No Application or ApplicationSet exists yet.** That is the next chunk. Note
  that ArgoCD will reconcile against a repository whose paths have moved:
  `core/ingress-controller/`, `core/load-balancer/` and every
  `projects/*/ingress.yaml` are gone.
- **`argocd-initial-admin-secret` still exists.** It can be deleted after the
  first login; deleting it does not change the password.
- **The Cloudflare gRPC zone setting is off.** Optional, and not needed for the
  CLI to work.
