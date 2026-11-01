# Monitoring

kube-prometheus-stack inside the cluster, watched from outside by a
healthchecks.io heartbeat ([ADR 0011](../../docs/adr/0011-monitoring-runs-in-the-cluster-and-a-heartbeat-outside-watches-it.md)).
A pinned helm render, synced by the Application `core-monitoring`.

```
./core/monitoring/render.sh      # after a values change or a chart bump
```

The render writes `kube-prometheus-stack.yaml` and `blackbox-exporter.yaml`
(synced by ArgoCD) and `crds.yaml` (applied by `kluster services seed`,
server-side). It refuses a render that holds a helm hook or a random value.

- **Stateless.** Prometheus keeps 3 days on an `emptyDir` (4 Gi, `retentionSize`
  3 GB); Grafana has no volume. A restart or a move loses the history.
- **No node pin.** Prometheus and Alertmanager prefer different nodes.
- **Nothing in kube-system.** etcd, the scheduler, the controller manager and
  kube-proxy bind their metrics to `127.0.0.1` (kubeadm), so their scrapes are
  off. CoreDNS is scraped through kubeadm's own `kube-dns` Service.
- **No admission webhook.** Its certificate comes from helm-hook Jobs that
  selfHeal would undo; `check-rules.sh` (CI `rules-check.yml`) takes its place.
- **Rules.** The chart's rules minus the deny list in the values file
  (`defaultRules.disabled`, each with its reason; read it again on every chart
  upgrade). The custom rules are in `rules/`, each with promtool unit tests in
  `rules/tests/`: WAL archiving, CNPG health and volume fill, certificates.
  `./core/monitoring/check-rules.sh` parses every rule and runs the tests.
- **Monitors.** `monitors/` scrapes what the chart does not know: the
  PostgreSQL instances, cert-manager, ArgoCD (a PodMonitor, so core/argocd
  needs no metrics Services) and Envoy Gateway (controller and proxies).
- **Dashboards.** Only from git. `dashboards/fetch.sh` downloads the pinned
  ArgoCD, cert-manager, CNPG and Envoy Gateway dashboards and checks each
  sha256; the kustomization turns each JSON into a ConfigMap the sidecar loads.
  A dashboard made in the UI is lost on restart unless its JSON is exported
  into `dashboards/` and listed in the kustomization.
- **Probes.** blackbox-exporter asks every public host from inside the
  cluster, out through Cloudflare and back. `probes/public-hosts.yaml` holds
  one Probe per expected status (a module in `blackbox-exporter-values.yaml`;
  no module follows a redirect); `PublicHostDown` (`rules/probes.yaml`) pages
  after 5 minutes. `kluster services check` fails when a host of a synced
  HTTPRoute has no target, or a target has no synced route: a new route adds
  its host here in the same change.
- **Grafana is public** at `grafana.tosak.net` (`httproute.yaml`), behind
  Cloudflare Access and then its own login. The Access application and its
  Emails policy are made in the Cloudflare dashboard; the proxied A record by
  hand, as for every host. Its probe (module `http_access`) passes only on the
  302 to the Access login, so it fails when the gate is gone, even if Grafana
  still answers 302 to `/login`. Prometheus and Alertmanager have no route.
- **Every image pinned by digest** in the values files. A chart bump needs new
  digests: `docker buildx imagetools inspect <image>:<tag>`.
- **Grafana's admin login** is the Secret `monitoring/grafana-admin` (Secret
  Catalog), read through `admin.existingSecret`.
