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

echo "[0/3] Install containerd"
run "wget -qO- https://github.com/containerd/containerd/releases/download/v2.2.0/containerd-2.2.0-linux-amd64.tar.gz | tar -C /usr/local -xz"
run "mkdir -p /usr/local/lib/systemd/system"
run "curl -fsSL https://raw.githubusercontent.com/containerd/containerd/main/containerd.service -o /usr/local/lib/systemd/system/containerd.service"
run "systemctl daemon-reload"
run "systemctl enable --now containerd"
check "containerd installed"
pause

echo "[1/3] Configure containerd"
run "mkdir -p /etc/containerd"
run "containerd config default > /etc/containerd/config.toml"
run "sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml"
run "systemctl restart containerd"
check "containerd configured"
pause

echo "[2/3] Install runc and CNI"
run "wget -q https://github.com/opencontainers/runc/releases/download/v1.4.0/runc.amd64"
run "install -m 755 runc.amd64 /usr/local/sbin/runc"
run "mkdir -p /opt/cni/bin"
run "wget -q https://github.com/containernetworking/plugins/releases/download/v1.9.0/cni-plugins-linux-amd64-v1.9.0.tgz"
run "tar -C /opt/cni/bin -xzvf cni-plugins-linux-amd64-v1.9.0.tgz"
check "runc & CNI installed"
pause
