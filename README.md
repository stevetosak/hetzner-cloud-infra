# Hetzner Cloud Infrastructure + K8S

## Infrastructure

The main idea was to create a cost effective HA k8s cluster for my personal projects. Hetzner is very cheap, this whole set up costs about ~20 eur a month which include 4 [cx23](https://www.hetzner.com/cloud) servers and a Load Balancer.
The servers are configured so 1 server is the control plane while the other 3 are worker nodes. The worker node's configuration is automated via scripts, so the nodes can be conisdered to be ephemeral.

I use Terraform to manage and automate my infrastructure components.

### Automation scripts
`infra/scripts/bootstrap` holds the original bootstrap scripts. **They are superseded and were not used to build the current cluster** — see `infra/scripts/README.md`, which lists the six defects found in them and says why they were deliberately left unrepaired. The cluster was rebuilt by hand against written procedures instead: `docs/runbook/control-plane.md`, `docs/runbook/workers.md` and `docs/runbook/cluster-services.md` (ADR 0004). `reset-nodes.sh` has been deleted.
Bootstrap SSH is gated by two separate Terraform variables, `allow_public_ssh_cp` and `allow_public_ssh_worker`, both defaulting to false. Each opens port 22 from the workstation's public IP on its own firewall. **Always read a firewall back from the Hetzner API after closing it** — the provider silently fails to remove a firewall's last rule (see `infra/README.md`).

### Issues with ephemeral nodes

Longhorn used to bind a node's name to a Disk UUID, so a recreated worker with a reused name referenced a disk that no longer existed. The fix at the time was to make every worker name unique with a timestamp.

**That fix destroyed the cluster.** Because each invocation minted a fresh suffix, one `node add` renamed every worker and therefore replaced all of them — and with Longhorn replicating across those three workers, replacing all three was a data deletion. Every database was lost on 2026-09-13.

Longhorn is gone (ADR 0005), so the constraint that forced unique names is gone with it. Worker names are now stable — `k8swk1`, `k8swk2`, `k8swk3` — and the whole `node_suffix` mechanism was deleted rather than repaired. PostgreSQL uses Hetzner Cloud Volumes, which outlive the server that mounts them.

### Networking

The nodes communicate via a private network in Hetzner. External access is done through SSH + VPN. I have Wireguard set up as vpn on the nodes, with the worker nodes + my local pc as peers. All wireguard vpn traffic goes through the control plane which acts as a proxy. I have multiple subnets to ensure some isolation and ease of networking rules if needed. The control plane, workers, and load balancer each reside in a seperate subnet.
A Hetzner Cloud Firewall is used to protect external acess to the nodes. I have it set up very strict, so the only way of accessing nodes is through the VPN.


## Kuberenetes
### Installation and Resources
- Kuberentes is managed and installed using [kubeadm](https://kubernetes.io/docs/reference/setup-tools/kubeadm/). 
- Container runtime for kubernetes used is [containerd](https://containerd.io/). 
- Storage is Hetzner Cloud Volumes through the [hcloud CSI driver](https://github.com/hetznercloud/csi-driver) — StorageClass `hcloud-volumes`, the cluster default. Longhorn was removed (ADR 0005): a Volume outlives the server that mounts it, so destroying every worker no longer destroys the data.
- Database currently used is Postgresql, administered with [CloudNativePG](https://cloudnative-pg.io/docs/1.28/)
- TLS certificates are managed by [cert-manager](https://cert-manager.io/). (Certificates are issued by LetsEncrypt)

### The Public Entry Point

**ingress-nginx is gone.** It was retired in March 2026 — no releases, no bugfixes and no
security fixes — and SIG Network tells every user to migrate. The rebuild was the only cheap
window, so the cluster moved to **Gateway API** with **Envoy Gateway**. See
`docs/adr/0008-gateway-api-and-the-public-entry-point.md`.

There is **one** Public Entry Point, not two ingress classes:

| | |
|---|---|
| Controller | Envoy Gateway, namespace `envoy-gateway-system` |
| Gateway | `tosak`, namespace `gateway`, class `tosak` |
| Listeners | HTTP :80 (redirects to HTTPS) and HTTPS :443 |
| Certificate | one wildcard, `*.tosak.net` + the apex, DNS-01 through Cloudflare |
| Load balancer | Hetzner `tosak-lb`, private `10.0.4.2`, PROXY protocol on |

**An application adds an `HTTPRoute`, not an `Ingress`.** The route lives beside the
workload; the certificate and the listeners live in `gateway`, because a listener's
certificate must be a Secret in the Gateway's own namespace. The Gateway's `allowedRoutes`
names which namespaces may attach — a control Ingress never had, where any namespace could
claim any host.

**The private ingress class was not rebuilt.** Its only consumers were the Longhorn UI,
which ADR 0005 removes, and a test. A VPN-only host will be built again when something
actually needs one.

#### Two settings that must always move together

`load-balancer.hetzner.cloud/uses-proxyprotocol` on the `EnvoyProxy`, and `proxyProtocol` on
the `ClientTrafficPolicy`. They are one setting in two places. PROXY headers arriving at a
listener that does not expect them break every connection, and so does the reverse.

#### Client addresses

Every host is proxied by Cloudflare, so the address the PROXY header carries is a Cloudflare
edge address. The real client comes from `CF-Connecting-IP`, which
`ClientTrafficPolicy.clientIPDetection` reads.

🔴 That header is forgeable until the origin is locked with Cloudflare Authenticated Origin
Pulls using a **per-zone** certificate. **This is not configured yet.** Do not reach for a
`SecurityPolicy` IP allowlist instead: `clientCIDRs` matches the *detected* address, so it
would check the forgeable header against itself.
