# core/hcloud — Hetzner cloud-controller-manager and CSI driver

Two cluster-wide components, both talking to the Hetzner API with the same
credentials.

| File | Source | Version |
|---|---|---|
| `ccm.yaml` | chart `hcloud-cloud-controller-manager` | v1.37.0 |
| `ccm-values.yaml` | the values that produced it | — |
| `csi.yaml` | chart `hcloud-csi` | v2.23.0 |
| `csi-values.yaml` | the values that produced it | — |
| `storageclasses.yaml` | hand-written: `hcloud-volumes` and `hcloud-volumes-retain` | — |

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

Volumes are independent of the servers that mount them, so destroying every
worker no longer destroys the data (ADR 0005).

## Hetzner servers only (ADR 0016)

A Hetzner volume attaches only to a Hetzner server in its own location, and
the CSI node binary exits on a machine where Hetzner's metadata service gives
it no server ID. So `csi-values.yaml` keeps the node DaemonSet on Nodes the CCM
labelled `instance.hetzner.cloud/provided-by=cloud`, and switches the chart's
own StorageClass off (`storageClasses: []`). `storageclasses.yaml` holds both
classes instead: `hcloud-volumes`, the default (`Delete`), and
`hcloud-volumes-retain`, the class every PostgreSQL volume is made from
(`Retain`). Each allows only `csi.hetzner.cloud/location` `hel1`, so a claim is
never provisioned for a Node without the driver
([external-provisioner#544](https://github.com/kubernetes-csi/external-provisioner/issues/544)).
That value is the name of the `hcloud` site in `infra/nodes/<env>.yaml`;
kluster's `csi` Stage refuses a file that disagrees, and a test holds them
equal.

`allowedTopologies` and the annotations change in place with `kubectl apply`.
`provisioner`, `parameters`, `reclaimPolicy` and `volumeBindingMode` cannot
change: the class is deleted and applied again by hand, with the operator's
yes.

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
kubectl apply -f core/hcloud/storageclasses.yaml
```

## Re-rendering after a version bump

Both charts are pulled from Hetzner's chart repository first and rendered
from the local copy (the `oci://ghcr.io` path answered 403 on 2026-11-06, and
`helm template` on an `oci://` chart prints its pull messages into the
render). Both renders take a values file: `csi-values.yaml` holds the CSI
containers' resources (ADR 0013).

```sh
helm pull hcloud-cloud-controller-manager --repo https://charts.hetzner.cloud \
  --version <new-version> --untar --untardir /tmp/charts
helm pull hcloud-csi --repo https://charts.hetzner.cloud \
  --version <new-version> --untar --untardir /tmp/charts

helm template hccm /tmp/charts/hcloud-cloud-controller-manager \
  --namespace kube-system \
  --values core/hcloud/ccm-values.yaml > core/hcloud/ccm.yaml

helm template hcloud-csi /tmp/charts/hcloud-csi \
  --namespace kube-system \
  --values core/hcloud/csi-values.yaml > core/hcloud/csi.yaml
```

Then check the diff before committing: the route-controller setting in
`ccm.yaml`, and in `csi.yaml` no StorageClass and the node DaemonSet's
`provided-by In [cloud]` affinity.
