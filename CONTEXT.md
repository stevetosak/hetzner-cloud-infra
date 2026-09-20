# Context — hetzner-cloud-infra

Glossary for the cluster and its automation. Terms only. No procedures, no
implementation detail, no status.

## Node

**Ambiguous. Do not use unqualified.**

Historically "node" meant any server in the cluster. Every piece of automation
in this repo that says "node" — `kluster node add`, `kluster node remove`,
`kluster node list`, `kluster reset`, `kluster bootstrap`,
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
