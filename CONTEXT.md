# Context — hetzner-cloud-infra

Glossary for the cluster and its automation. Terms only. No procedures, no
implementation detail, no status.

## tosak

The organisation, and the owner of the cluster. Shared infrastructure carries
this name: `tosak-net`, `tosak-cp-firewall`, `tosak-worker-firewall`,
`tosak-cluster`, `tosak-cp-ip`, `tosak-lb`, `tosak-pg-cluster`, and
`k8s-cp.tosak.internal`.

Not an application. Everything named `tosak-*` serves every application, and
one application's needs never decide its shape.

## Application

A workload the cluster hosts: **authos**, **doma**, **wasteio**, **imaps**. An
application names only what is its own — its namespace, its hosts and Routes,
its own database, its ArgoCD project.

Shared infrastructure once carried `authos-*` names, from when Authos was the
only tenant. Three other applications then held a connection string naming a
fourth. The names were corrected during the 2026-09-20 rebuild, while none of
the resources existed (ADR 0007). Anything written before that date names them
the old way; match by ID.

_Avoid_: project, tenant, service.

## Node

**Ambiguous. Do not use unqualified.**

Historically "node" meant any server in the cluster. Every piece of automation
in this repo that says "node" — `kluster node add`, `kluster node remove`,
`kluster node list`, `kluster reset`, `kluster bootstrap`, and the deleted
`infra/scripts/bootstrap/reset-nodes.sh` — operates on **Workers only**, and
cannot act on the Control Plane at all.

That gap between the word and the behaviour hid the fact that nothing in the
system could rebuild a Control Plane. Always write **Worker** or **Control
Plane**.

## Control Plane

The single server running the Kubernetes API server, scheduler, controller
manager and etcd. Also the WireGuard hub: every Worker and every operator
laptop peers with it, and it is the address operators reach the cluster
through.

Not interchangeable with Worker. It runs no application workload and no
Longhorn storage. It is the only server whose identity — its private address,
its primary public IP and its VPN address — other things are configured to
expect, so replacing it is never transparent.

## Worker

A server that runs application workloads. Deliberately **ephemeral**: Workers
are created, destroyed and recreated as a routine operation. Worker names are
stable and reused (`k8swk1`, `k8swk2`, `k8swk3`) — a Worker's identity is its
name, not the server currently answering to it.

"Ephemeral" describes Workers only. It has never been true of the Control
Plane.

### A Worker is ephemeral. The Worker set is not.

The distinction is not pedantry; conflating the two is what made this cluster's
loss expensive. Destroying one Worker is routine. Destroying **all** of them at
once is only routine if no durable data lives on them — which is why data now
lives on volumes that are not part of a Worker's lifetime.

Always say which you mean.

## Private Network

The Hetzner network `tosak-net`, `10.0.0.0/16`, reached on each server's second
interface. It carries **all cluster-internal traffic**: kubelet to API server,
API server to kubelet, pod to pod, and load balancer to the Public Entry
Point.

It is an addressing rule, not a security boundary. A Hetzner firewall filters
the public interface only, so traffic inside the Private Network passes
unfiltered. "It is private" says where traffic goes, never that something stops
it going elsewhere.

_Avoid_: internal network, the 10.0 network.

## VPN

The WireGuard overlay `10.100.0.0/24`. It carries **operator traffic only** —
reaching the API server and SSH to any server.

The Control Plane is its hub and also its router: an operator reaches a Worker
because the Control Plane forwards between two of its own peers. A Worker holds
one tunnel, to the Control Plane, and no Worker peers with another Worker.

Because SSH rides the VPN, port 22 stays closed on every public interface
outside a bootstrap.

_Avoid_: WireGuard network, the tunnel, wg0.

## Public Interface

The routable address on each server. It carries **outbound traffic only** —
package installs, image pulls — plus the WireGuard endpoint, which is the one
port open to the world.

No cluster component addresses another over it. A component that does has been
misconfigured, and nothing in the infrastructure will report it.

_Avoid_: public IP, external interface.

## Control Plane Endpoint

`k8s-cp.tosak.internal`, the cluster's own name for its API server. Every node
resolves it to the Control Plane's private address; the name is written into the
API server certificate and into the record joining nodes read.

It exists so the API server can move without the cluster being rebuilt. A second
Control Plane means repointing the name at a load balancer that fronts both —
certificates and node configuration are already correct. Without the name, the
same change re-issues every certificate.

Distinct from two addresses it is often confused with. The **advertise address**
is where the API server listens, on the Private Network. The **operator route**
is `10.100.0.1`, over the VPN, and is what an operator's kubeconfig names. All
three appear in the certificate; only the Endpoint is what the cluster calls
itself.

Not in public DNS. `.internal` is reserved for private use, and the name resolves
from each node's own hosts file, so the cluster can start with no external
resolver.

_Avoid_: the API server address, the cluster URL.

## Public Entry Point

The single place traffic from outside enters the cluster. It owns the listeners,
the certificate and the load balancer, and it states which namespaces may
attach a Route to it.

There is one, because there is one public address. It belongs to the
organisation and not to any application, so no application configures it and no
application holds its certificate.

Distinct from the **Public Interface**, which is a server's own address and
carries no inbound traffic but WireGuard. A visitor reaches an application
through the Public Entry Point; a server reaches a package mirror through its
Public Interface.

_Avoid_: ingress, the ingress controller, the load balancer.

## Route

An application's claim on a hostname and a path, and the statement of which of
its Services serves them. It lives beside the workload it routes to, in the
application's own namespace, and the Public Entry Point must permit it.

A Route carries no certificate and no listener. Those belong to the Public Entry
Point. The split is deliberate: an application declares what it answers to, and
the organisation decides what is exposed.

Written as `HTTPRoute`. `Ingress` is retired (ADR 0008) and anything still
written as an Ingress is from before the 2026-09-20 rebuild.

_Avoid_: ingress, ingress rule.

## Reset

Destroying and recreating **every Worker**, then re-bootstrapping them. A
routine, expected operation. Does not touch the Control Plane.

## Bootstrap

Bringing a freshly created server from bare Ubuntu to cluster membership.
Worker bootstrap and Control Plane bootstrap are different procedures with
different stages; only Worker bootstrap is currently automated.

## Deploy Catalog

The record of what version of which application is live. Written by ArgoCD's
`on-deployed` signal, not by the deploying repository.

## Module

One of three Terraform root modules, each with its own state: **shared**
(network, subnets, firewall, keys), **control-plane**, **workers**. They refer
to each other's resources by name, never by shared state. The split exists so
that a Worker operation cannot express the Control Plane.

"The terraform config" is no longer a single thing. Always name the module.

## Plan Mode

kluster's default behaviour: compute a Terraform plan, print it, check it
against the command's declared intent, and change nothing. Acting requires an
explicit `--apply`.

kluster is a convenience over Terraform and the runbook — never the authority
on how the cluster is built.

## Intent Assertion

A command's declaration of what its plan should contain — `node add` expects
one create and no destroys. A plan that disagrees is a warning in Plan Mode and
a refusal under `--apply`.

## Restore Drill

A scheduled, automated restore of the latest database backup into a throwaway
cluster, asserting the data is really there. Distinct from a backup **job**,
which only proves that writing to the bucket succeeded.

An unverified backup is not a backup.
