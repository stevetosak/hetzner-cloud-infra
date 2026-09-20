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
