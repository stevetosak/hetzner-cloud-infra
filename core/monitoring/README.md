# Monitoring

kube-prometheus-stack inside the cluster, watched from outside by a
healthchecks.io heartbeat ([ADR 0011](../../docs/adr/0011-monitoring-runs-in-the-cluster-and-a-heartbeat-outside-watches-it.md)).
A pinned helm render, synced by the Application `core-monitoring`.

```
./core/monitoring/render.sh      # after a values change or a chart bump
```

The render writes `kube-prometheus-stack.yaml` (synced by ArgoCD) and
`crds.yaml` (applied by `kluster services seed`, server-side). It refuses a
render that holds a helm hook or a random value.

- **Stateless.** Prometheus keeps 3 days on an `emptyDir` (4 Gi, `retentionSize`
  3 GB); Grafana has no volume. A restart or a move loses the history.
- **No node pin.** Prometheus and Alertmanager prefer different nodes.
- **Nothing in kube-system.** etcd, the scheduler, the controller manager and
  kube-proxy bind their metrics to `127.0.0.1` (kubeadm), so their scrapes are
  off. CoreDNS is scraped through kubeadm's own `kube-dns` Service.
- **No admission webhook.** Its certificate comes from helm-hook Jobs that
  selfHeal would undo; a CI `promtool check rules` takes its place.
- **Every image pinned by digest** in the values file. A chart bump needs new
  digests: `docker buildx imagetools inspect <image>:<tag>`.
- **Grafana's admin login** is the Secret `monitoring/grafana-admin` (Secret
  Catalog), read through `admin.existingSecret`.
