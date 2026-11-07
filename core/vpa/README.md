# core/vpa — the Vertical Pod Autoscaler

VPA 1.8.1 from the chart `vertical-pod-autoscaler` 0.15.0
(<https://kubernetes.github.io/autoscaler>), as ADR 0014 decides: it sets the
memory and CPU requests of the cluster's own workloads. Synced by the
Application `core-vpa`.

| File | Applied by |
|---|---|
| `vertical-pod-autoscaler.yaml` | Argo CD (render of the chart; do not edit) |
| `vpa.yaml` | Argo CD: the objects of the kube-system and kube-flannel workloads and of VPA itself |
| `crds.yaml` | `kluster services seed`, server-side |
| `namespace.yaml` | `kluster services seed` |
| `vpa-values.yaml` | render input |

Every other workload has its `VerticalPodAutoscaler` object next to it, in
`core/<dir>/vpa.yaml` or in its overlay under `projects/`.

## Re-rendering

```sh
./core/vpa/render.sh
```

It pulls the pinned chart, fails on a helm hook or on two renders that
differ, and splits the CRDs into `crds.yaml`. The webhook certificate comes
from cert-manager (a self-signed CA Issuer in `vpa`), so the render holds no
key.
