# core/cni — Flannel

`flannel.yaml` is upstream `kube-flannel.yml` v0.28.9 with two local edits. The
header comment in the file names them; read it before any upgrade, because a
straight re-download drops both.

- **`Backend.MTU = 1450`** — this is the **UNDERLAY** MTU, the MTU of the
  Hetzner private network. Flannel subtracts VXLAN's 50 bytes itself, so pods
  get 1400. The file said `1400` once, reasoning that "the network is 1450 and
  VXLAN costs 50" — which subtracts twice and gives pods 1350. Flannel also
  does not resize an existing VXLAN device, so correcting it needed
  `ip link delete flannel.1` and a restart, not just a re-apply.
- **`--iface-regex=^10\.0\.`** — binds flanneld to the private NIC. Without it
  flannel takes the first interface it finds, which is the public one: pod
  traffic would leave the private network and the derived MTU would be wrong.
  The pattern matches `10.0.x.x` only. The public address, the WireGuard
  address `10.100.0.1` and loopback all fail to match.

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

Then re-apply both edits and keep the header comment.
