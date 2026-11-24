#!/usr/bin/env bash
# The k3d test of node-controller: the real API calls against a real API
# server, with the real RBAC of core/nodes/node-controller.yaml. It makes a
# throw-away k3s cluster (one server, two agents), runs k3d_test.go against it
# and deletes the cluster again, also on a failure. CI runs it
# (.github/workflows/node-controller.yml); run it from anywhere:
#
#   tools/kluster/internal/nodecontroller/k3d-test.sh
#
# Needs docker, k3d and go. Takes about five minutes: a Node with no kubelet
# has to go not Ready first.
set -euo pipefail
cd "$(dirname "$0")/../.."

readonly image='rancher/k3s:v1.37.2-k3s1@sha256:21ea6e0fac4e3930c2ce8aa40ddd41a8b18cb3e9a0222c030dc005e7e88c4332'
readonly name="node-controller-test-$$"
kubeconfig="$(mktemp)"
trap 'k3d cluster delete "$name" >/dev/null 2>&1 || true; rm -f "$kubeconfig"' EXIT

# k3s's own cloud controller would delete a Node it does not know; the rest
# is left out to keep the cluster small.
k3d cluster create "$name" --image "$image" --agents 2 --no-lb --wait --timeout 180s \
	--k3s-arg '--disable=traefik,servicelb,metrics-server,local-storage@server:0' \
	--k3s-arg '--disable-cloud-controller@server:0' \
	--kubeconfig-update-default=false --kubeconfig-switch-context=false
k3d kubeconfig get "$name" >"$kubeconfig"

K3D_KUBECONFIG="$kubeconfig" go test -count=1 -tags k3d -run '^TestK3d' -timeout 15m -v ./internal/nodecontroller/
