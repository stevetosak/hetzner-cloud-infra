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

NEW_USER="tosak"

echo "[1/4] Create user"
run "adduser --disabled-password --gecos '' $NEW_USER"
run "usermod -aG sudo $NEW_USER"
run "mkdir -p /home/$NEW_USER/.ssh"
run "cp /root/.ssh/authorized_keys /home/$NEW_USER/.ssh/authorized_keys"
run "chmod 600 /home/$NEW_USER/.ssh/authorized_keys"
run "chown -R $NEW_USER:$NEW_USER /home/$NEW_USER/.ssh"
check "User created and SSH key copied"
pause

echo "[2/4] Sysctl configuration"
run "cat <<EOF > /etc/sysctl.d/k8s.conf
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
EOF"
run "sysctl --system"
check "Sysctl values applied"
pause

echo "[3/4] Disable swap"
run "swapoff -a"
run "sed -i '/ swap / s/^/#/' /etc/fstab"
check "Swap disabled"
pause

echo "[4/4] Load kernel modules"
run "cat <<EOF > /etc/modules-load.d/k8s.conf
overlay
br_netfilter
EOF"
run "modprobe overlay"
run "modprobe br_netfilter"
check "Kernel modules loaded"
pause
