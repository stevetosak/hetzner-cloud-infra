# core/cni — Flannel

`flannel.yaml` is upstream `kube-flannel.yml` v0.28.9 with three local edits. The
header comment in the file names them; read it before any upgrade, because a
straight re-download drops all three.

- **`Backend.MTU = 1400`** — this is the **UNDERLAY** MTU: the WireGuard mesh
  tunnel's, the smallest link a pod packet crosses between sites (ADR 0016).
  Flannel subtracts VXLAN's 50 bytes itself, so pods get 1350, which is
  `podMTU` in `kluster.yaml`; a kluster test refuses the two out of step.
  Writing the pod value here subtracts twice. Until 2026-11-22 the value was
  1450, the Hetzner private network, and pods got 1400. Flannel does not
  resize an existing VXLAN device, so a running cluster changes with
  `kluster node pod-mtu`
  ([Workers runbook](../../docs/runbook/workers.md#change-the-pod-mtu)), not
  with a re-apply.
- **`--iface-regex=^10\.0\.`** — binds flanneld to the private NIC. Without it
  flannel takes the first interface it finds, which is the public one: pod
  traffic would leave the private network and the derived MTU would be wrong.
  The pattern matches `10.0.x.x` only. The public address, the WireGuard
  address `10.100.0.1` and loopback all fail to match.
- **The kube-flannel container's resources** — `25m` / `32Mi`, limit `64Mi`,
  sized by ADR 0013 from measured peaks. Upstream's `100m` / `50Mi` comes back
  with a re-download.

Pod CIDR `10.244.0.0/16` is upstream's own default and already matches
`kubeadm init --pod-network-cidr` (ADR 0001).

Flannel enforces no NetworkPolicy. The cluster has none today. If policy is
ever needed, that is the trigger to move to Cilium, and `10.245.0.0/16` is held
unallocated so Cilium's per-node migration stays available (ADR 0001).

## Apply

```sh
kubectl apply -f core/cni/flannel.yaml
```

Before the CNI is up the control-plane node reports `NotReady` and CoreDNS
stays `Pending`. That is expected, not a fault.

## Upgrading

```sh
curl -fLO https://github.com/flannel-io/flannel/releases/download/<tag>/kube-flannel.yml
```

Then re-apply all three edits and keep the header comment.
