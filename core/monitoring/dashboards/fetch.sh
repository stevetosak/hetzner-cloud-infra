#!/usr/bin/env bash
# Downloads the pinned upstream Grafana dashboards into core/monitoring/dashboards/.
#
# Run from anywhere:  ./core/monitoring/dashboards/fetch.sh
#
# Grafana is stateless (ADR 0011): a dashboard exists only as a JSON file here,
# which kustomization.yaml turns into a ConfigMap with the label
# grafana_dashboard: "1"; the chart's sidecar loads it. The chart's own
# Kubernetes dashboards come with the render.
#
# Each entry is pinned to the version the cluster runs and to the sha256 of
# the download; a mismatch stops the script. To upgrade: change the URL, run,
# read the error, put the new sha256 in, run again, and read the diff.
# A dashboard made in the UI survives only when its JSON is exported to here.
set -euo pipefail

cd "$(dirname "$0")"

# name | URL | sha256 of the download
DASHBOARDS="
argocd|https://raw.githubusercontent.com/argoproj/argo-cd/v3.5.3/examples/dashboard.json|d25cc5a9f7d1c7409ed55b5d615a5b39775bc6e07389a00b69a9cb78c1e8bbd6
cert-manager|https://grafana.com/api/dashboards/20842/revisions/3/download|a763aa244c456ce087ac275af4fa97043947c630842e8fa6cc4dfe825155f9e2
cnpg|https://raw.githubusercontent.com/cloudnative-pg/grafana-dashboards/cluster-v0.0.5/charts/cluster/grafana-dashboard.json|676b10ae3a8850108d484e4491739207b2ad1ebca44300472168d81a8788e4fb
envoy-gateway-global|https://raw.githubusercontent.com/envoyproxy/gateway/v1.9.1/charts/gateway-addons-helm/dashboards/envoy-gateway-global.json|2cdc2c605b270ad2843708ad1c5defb787cf7d4d43373261188f11871f1bd94e
envoy-proxy-global|https://raw.githubusercontent.com/envoyproxy/gateway/v1.9.1/charts/gateway-addons-helm/dashboards/envoy-proxy-global.json|bde5d6326921d45a0807e523a2ce496fd6460fd93ab356b1d4118ae149d586ef
envoy-clusters|https://raw.githubusercontent.com/envoyproxy/gateway/v1.9.1/charts/gateway-addons-helm/dashboards/envoy-clusters.json|896e598739c562e8221bb5a7fe660a94a62e9d68c02acfb4381491dd453110be
"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

for entry in $DASHBOARDS; do
  IFS='|' read -r name url sum <<<"$entry"
  curl -fsSL -o "$TMP/$name.json" "$url"
  got="$(sha256sum "$TMP/$name.json" | cut -d' ' -f1)"
  if [ "$got" != "$sum" ]; then
    echo "$name: sha256 $got, expected $sum ($url)" >&2
    exit 1
  fi
done

# The CNPG dashboard is a grafana.com export: its one expression panel names
# the import-time input ${DS_EXPRESSION}, which only the import dialog fills.
# A provisioned dashboard needs the built-in uid. ${DS_PROMETHEUS} stays: the
# dashboard declares it as a datasource variable of its own.
sed -i 's/"\${DS_EXPRESSION}"/"__expr__"/' "$TMP/cnpg.json"

for entry in $DASHBOARDS; do
  name="${entry%%|*}"
  mv "$TMP/$name.json" "$name.json"
done
echo "wrote $(echo "$DASHBOARDS" | grep -c '|') dashboards"
