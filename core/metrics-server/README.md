# metrics-server

Serves the metrics API (`metrics.k8s.io`): `kubectl top` today, a
HorizontalPodAutoscaler later. It reads the kubelets' resource metrics and
keeps no history; Prometheus does that
([ADR 0011](../../docs/adr/0011-monitoring-runs-in-the-cluster-and-a-heartbeat-outside-watches-it.md)).
Installed by the [Capacity Plan](../../docs/plans/capacity-right-size-then-grow-by-cost.md).
A pinned helm render, synced by the Application `core-metrics-server`.

```
./core/metrics-server/render.sh      # after a values change or a chart bump
```

The render writes `metrics-server.yaml`. It records the sha256 of the chart
archive (a classic helm repository prints no digest), refuses a helm hook, and
renders twice to prove the output does not change between renders.
`namespace.yaml` is applied by `kluster services seed`.

- **Kubelet TLS is not verified** (`--kubelet-insecure-tls`). The kubelets
  serve self-signed certificates (kubeadm without `serverTLSBootstrap`); the
  Prometheus kubelet scrape skips verification for the same reason.
  CA-signed kubelet certificates would remove both.
- **One object in kube-system.** The chart binds its ServiceAccount to
  `extension-apiserver-authentication-reader`, a Role that lives only in
  `kube-system`. So the `core` AppProject admits `kube-system` as a
  destination, and the seed's namespace check accepts it
  (`clusterNamespaces` in `tools/kluster/internal/seed/plan.go`).
- **An APIService.** The `core` AppProject allows `APIService` cluster-wide.
  While the pod is down, `v1beta1.metrics.k8s.io` is not Available and API
  discovery reports it as an error; nothing else breaks.
- **Check it:**
  `kubectl get apiservice v1beta1.metrics.k8s.io` shows `Available True`,
  and `kubectl top nodes` answers.
