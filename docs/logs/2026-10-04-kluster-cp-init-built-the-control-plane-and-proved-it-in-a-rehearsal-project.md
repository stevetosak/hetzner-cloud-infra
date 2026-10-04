---
title: "kluster cp init built the Control Plane and proved it in a rehearsal project"
description: "The kluster cp init command builds a Control Plane into an empty slot, and five rehearsal runs in a separate Hetzner project found and fixed five faults before a clean run."
type: log
date: 2026-10-04
topics: [provisioning, networking, kubernetes]
---

This chunk added `kluster cp init` to `tools/kluster`, the second step of Phase 6 (see [ADR 0009](../adr/0009-kluster-builds-the-whole-cluster-and-proves-it-in-a-rehearsal-project.md)). The command carries out the [control-plane runbook](../runbook/control-plane.md) as Stages, and it was proven by a rehearsal build from an empty project. Before this chunk, the same session pushed chunk 1: PR #31 merged as `3602196`, PR #10 closed with a link, and the safety-core step was ticked in the Plan. The work is three commits on `feat/kluster-cp-init`; no live resource was changed.

## Decisions taken first

The operator chose, on 2026-10-04, that the VPN proof is kluster's own in-process WireGuard peer (wireguard-go netstack, no root). The edit of the laptop `wg0.conf` is a Stage: live, it writes `/etc/wireguard/wg0.conf` through sudo and restarts `wg-quick@wg0`; in a rehearsal, it writes a stand-in under `~/.config/kluster/rehearsal/`. Diffs use go-udiff. Flannel, the cloud controller, the CSI driver and the `hcloud` Secret are applied with kubectl on the Control Plane over SSH. The kubeconfig comes from a kubectl subprocess: live merges it as `tosak-admin@tosak`, a rehearsal writes a separate file. The ADR 0009 amendment records that a rehearsal never touches the operator's own WireGuard or kubeconfig.

## What was built

New packages in `tools/kluster/internal/`: `filediff` (secret lines redacted to a sha256 prefix), `wgconf` (a hand-written in-place editor of the hub peer, because INI libraries drop comments and repeated `[Peer]` blocks), `vpn` (the netstack peer), `kubeconfig` (renames `admin.conf` and checks `/readyz` through any dialer), `local`, `routeproof`, `provision/controlplane.go` and `cloud/server.go`. The Stages are `private-network`, `base-host`, `wireguard-hub`, `kube-prep`, `kubeadm-init`, `cni-flannel`, `cloud-controller`, `csi`, `complete-build`, `laptop-wireguard` and `laptop-kubeconfig`. The CNI is its own Stage, so Cilium can replace Flannel later (ADR 0001). `kluster ssh open` was added for a rehearsal only.

A slot counts as empty through a marker: cloud-init writes `/etc/kluster/cp-init = running`, and the last Stage writes `complete`. A `k8s-cp` without a kluster pin, or without `running`, is refused. The config gained `controlPlane.name`, `cluster.{podCIDR,serviceCIDR,hcloudNetwork,podMTU}`, `wireguard.probeIP` (10.100.0.254), `versions.cniPlugins`, `manifests.*` and `envs.<env>.laptop.*`. Validation refuses a rehearsal laptop block that names live paths, sudo, a merge or a unit.

The runbook `docs/runbook/control-plane.md` has a new section, "How kluster carries this out" (a differences table, the trap, the route proof, the workstation edits). `docs/runbook/rehearsal.md` describes the cp init run.

## What was proven

- Live refusal: `kluster cp init` in Plan Mode against env live answered "k8s-cp exists at 46.62.209.249 and kluster did not create it: the slot is not empty", before any Terraform. `kluster ssh open` is refused for live. Both were run.
- Plan Mode in project `tosak-rehearsal` showed exactly one firewall update, one create and every Stage.
- Run 5 of `cp init --apply` completed. The route proof printed "SSH as cp-dev over the tunnel; the hub saw the client as 10.100.0.254" and "/readyz answers ok … over the tunnel". The stand-ins were written and the marker read `complete`. A second run said "build is complete, nothing to do".
- Checked by hand after run 5: the hub holds one peer (the laptop), so kluster's own peer was removed; the node is Ready at 10.0.1.5 / 46.62.202.184; all kube-system pods run except `hcloud-csi-controller`, which stays Pending by design; the stand-in key equals the host's `/etc/wireguard/public.key`.
- Clean proof: `down` left the project empty, then `shared --apply` and ONE `cp init --apply` from empty (server id 168628579) exited 0. The Stages took about three minutes and the stand-in `wg0.conf` was edited with a `.bak-kluster-*` backup. `down --apply` printed "read back: the rehearsal project is empty", and a second listing was empty.
- `go test -race ./...` passes in all packages. The kubectl merge test runs the real kubectl.

## What went wrong

Five runs, first server id 168627333 at 46.62.202.184, each found a fault.

- 🔴 Run 1 panicked after the host key rotation. `knownhosts.New` reads the pins file once, so the callback made before the rotation refused the rotated key. The failed redial set the client to nil, and the deferred Close panicked and hid the error. The fix: `Pins.Callback` re-reads the file at each check (test `TestCallbackSeesPinsSetAfterItWasMade`), and Close is nil-safe. The SSH close still ran and read back closed.
- 🔴 Run 2: the dry run failed with empty output, because `kubeadm … | grep` hid the kubeadm error. Output now goes to a root-only log, and the tail shows on failure with join and token lines filtered.
- 🔴 Run 3: kubeadm preflight said "Port 2379/2380 is in use" with nothing listening. The cause was `enp7s0` down with no address: the `network-config.json` from Hetzner held `eth0` alone, and the kernel renamed eth1 to enp7s0 at 10:45:09, five seconds after boot at 10:45:04. The provider attaches the fixed private IP after it creates the server, and cloud-init configures it only if the attach lands before the first boot reads the metadata. The live Control Plane (2026-09-20) won this race (its netplan has enp7s0, checked read-only over the VPN), and an earlier clean rehearsal server won it too. The fix: Stage `private-network` writes `/etc/netplan/60-kluster-private.yaml`, matched by the MAC from the API, and reconfigures only that interface. The attach order is assumed from timing, not read in the provider source.
- 🔴 Run 4: `cloud-controller` acted, but the probe said "not yet: initialized". The check was `! export KUBECONFIG=… ⏎ kubectl … | grep -q uninitialized`, and `!` negated the `export`, so the check passed WITH the taint. The fix is in place, and `TestCloudControllerProbeInBash` (real bash, stub kubectl) was shown failing on the old check.
- `Gate.Open` refused an already-open firewall (a Required update on a clean plan), which would block any resume after a crash before the close. A clean plan with the API showing open is now accepted (test added).

cloud-init warns `ssh_genkeytypes: [] is too short`. The warning is a schema warning and recoverable; the seeded key still verified the first login. It was not changed.

## Not proven

- The live sudo path for `/etc/wireguard/wg0.conf` and the live kubeconfig merge with `/readyz` through the kernel `wg0` are unit-tested only. A rehearsal uses stand-ins by design.
- The old work tree `.claude/worktrees/feat+kluster-safety-core` is locked by a live session (pid 491570) and is not removed. The stand-in kubeconfig and `wg0.conf` stay under `~/.config/kluster/rehearsal/` after `down`; this is harmless, and `down` could clear them.

## Next

The operator approves the push of `feat/kluster-cp-init`, and the code PR opens. After the merge: `git worktree remove`, and sync master. Phase 6 chunk 3 is the Worker commands on the rehearsal Control Plane (`node add/remove/replace/list`, a rolling `reset`, tfvars written by kluster with a commit gate, and the five corrections in `docs/runbook/workers.md`). It reuses the `private-network` Stage, because the same race applies to Workers.
