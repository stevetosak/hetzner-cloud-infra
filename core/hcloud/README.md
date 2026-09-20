# core/hcloud — Hetzner cloud-controller-manager and CSI driver

Two cluster-wide components, both talking to the Hetzner API with the same
credentials.

| File | Source | Version |
|---|---|---|
| `ccm.yaml` | chart `hcloud-cloud-controller-manager` | v1.37.0 |
| `ccm-values.yaml` | the values that produced it | — |
| `csi.yaml` | chart `hcloud-csi` | v2.23.0 |

The YAML is rendered, not hand-written. Do not edit it in place: change the
values file and render again, so the file always matches a named chart version.

## Why both are needed

The kubelet runs with `--cloud-provider=external`, on the control plane as well
as on the workers. Kubernetes then taints each new node
`node.cloudprovider.kubernetes.io/uninitialized` and waits. **Until the CCM
runs, nothing schedules anywhere.**

The CCM clears that taint, writes each node's `providerID`, and puts the
server's private address on the Node object as InternalIP. The CSI driver needs
that `providerID` to attach a volume, so the order is CCM first, CSI second.

The CSI driver makes `hcloud-volumes` the **default** StorageClass, which is
where PostgreSQL now lives. Volumes are independent of the servers that mount
them, so destroying every worker no longer destroys the data (ADR 0005).

## The one setting that must not drift

`HCLOUD_NETWORK_ROUTES_ENABLED=false`, set in `ccm-values.yaml`.

The route controller is **on by default** as soon as a network is configured.
Left on, it writes pod-CIDR routes into `tosak-net`, and Hetzner rejects any
route destination outside that network's own `10.0.0.0/16`. The pod CIDR is
`10.244.0.0/16`, so every reconcile fails, every 30 seconds, for ever. Flannel
carries pod traffic over VXLAN instead (ADR 0001).

`ccm.yaml` still carries `--allocate-node-cidrs=true` and
`--cluster-cidr=10.244.0.0/16`. The chart ties those to the network being
enabled and gives no way to drop them. Only the route controller reads them, so
with it disabled they do nothing beyond one startup warning that cloud routes
will not be configured. That warning is expected.

## Prerequisite secret

Both components read one Secret in `kube-system`:

```sh
kubectl -n kube-system create secret generic hcloud \
  --from-literal=token=<hetzner-api-token> \
  --from-literal=network=tosak-net
```

Phase 5 replaces this hand-made secret with an age-encrypted
`credentials.enc.yaml` committed next to it. A secret that exists only in the
cluster has not been restored, only recreated (ADR 0003).

## Apply

```sh
kubectl apply -f core/hcloud/ccm.yaml
kubectl apply -f core/hcloud/csi.yaml
```

## Re-rendering after a version bump

```sh
helm template hccm \
  oci://ghcr.io/hetznercloud/charts/hcloud-cloud-controller-manager \
  --version <new-version> \
  --namespace kube-system \
  --values core/hcloud/ccm-values.yaml > core/hcloud/ccm.yaml

helm template hcloud-csi \
  oci://ghcr.io/hetznercloud/charts/hcloud-csi \
  --version <new-version> \
  --namespace kube-system > core/hcloud/csi.yaml
```

Then check the diff for the route-controller setting before committing.
