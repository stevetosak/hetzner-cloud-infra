#!/bin/bash
set -e

INTERACTIVE=false
DRY_RUN=false

for arg in "$@"; do
  case "$arg" in
    --interactive) INTERACTIVE=true ;;
    --dry-run) DRY_RUN=true ;;
  esac
done

source "/tmp/bootstrap_utils.sh"
KUBE_JOIN_COMMAND="$4"

echo "[1/4] Install Kubernetes packages"
run "apt-get update"
run "apt-get install -y apt-transport-https ca-certificates curl gpg chrony"
run "mkdir -p /etc/apt/keyrings"
run "curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.34/deb/Release.key | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg"
run "echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.34/deb/ /' > /etc/apt/sources.list.d/kubernetes.list"
run "apt-get update"
run "apt-get install -y kubelet kubeadm kubectl"
run "apt-mark hold kubelet kubeadm kubectl"
check "Kubernetes packages installed"
pause

echo "[2/4] Configure kubelet"
PRIVATE_IP=$(ip -4 addr show enp7s0 | grep -oP '(?<=inet\s)\d+(\.\d+){3}' || true)
if [[ -z "$PRIVATE_IP" ]]; then
  echo "❌ Failed to detect IP on enp7s0"
  exit 1
fi
run echo "KUBELET_EXTRA_ARGS=--node-ip=${PRIVATE_IP}" \
      > /etc/default/kubelet

check "kubelet will advertise IP: $PRIVATE_IP"
pause

echo "[3/4] Enable kubelet"
run "systemctl daemon-reexec"
run "systemctl enable kubelet"
check "Worker ready for kubeadm join"
pause

$KUBE_JOIN_COMMAND
