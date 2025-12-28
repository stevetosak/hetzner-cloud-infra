#!/bin/bash
set -e

INTERACTIVE=false
DRY_RUN=false
WG_DIR="/etc/wireguard"
WG_CONF="$WG_DIR/wg0.conf"

# -------------------------------
# Parse flags
# -------------------------------
for arg in "$@"; do
  case "$arg" in
    --interactive) INTERACTIVE=true ;;
    --dry-run) DRY_RUN=true ;;
  esac
done

# -------------------------------
# Accept arguments from bootstrap
# -------------------------------
CONTROL_PUBLIC_KEY="$1"
WORKER_VPN_IP="$2"
CONTROL_PLANE_IP="$3"

if [[ -z "$CONTROL_PUBLIC_KEY" || -z "$WORKER_VPN_IP" || -z "$CONTROL_PLANE_IP" ]]; then
    echo "❌ Missing required arguments: CONTROL_PUBLIC_KEY WORKER_VPN_IP CONTROL_PLANE_IP"
    exit 1
fi

pause() {
  if $INTERACTIVE; then
    echo
    read -p "Press ENTER to continue..."
    echo
  fi
}

check() {
  echo "✔ $1"
}

run() {
  if $DRY_RUN; then
    echo "[DRY-RUN] $*"
  else
    eval "$@"
  fi
}

NEW_USER="tosak"

# -------------------------------
# 0/9 Create user
# -------------------------------
echo "[0/9] Create user"

run "adduser --disabled-password --gecos '' $NEW_USER"
run "usermod -aG sudo $NEW_USER"

run "mkdir -p /home/$NEW_USER/.ssh"
run "cp /root/.ssh/authorized_keys /home/$NEW_USER/.ssh/authorized_keys"
run "chmod 600 /home/$NEW_USER/.ssh/authorized_keys"
run "chown -R $NEW_USER:$NEW_USER /home/$NEW_USER/.ssh"

check "User $NEW_USER created and root SSH key copied"
pause

# -------------------------------
# 1/9 Sysctl
# -------------------------------
echo "[1/9] Sysctl configuration"
run "cat <<EOF > /etc/sysctl.d/k8s.conf
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
EOF"
run "sysctl --system"
check "Sysctl values applied"
pause

# -------------------------------
# 2/9 Disable swap
# -------------------------------
echo "[2/9] Disable swap"
run "swapoff -a"
run "sed -i '/ swap / s/^/#/' /etc/fstab"
check "Swap disabled"
pause

# -------------------------------
# 3/9 Kernel modules
# -------------------------------
echo "[3/9] Load kernel modules"
run "cat <<EOF > /etc/modules-load.d/k8s.conf
overlay
br_netfilter
EOF"
run "modprobe overlay"
run "modprobe br_netfilter"
check "Kernel modules loaded"
pause

# -------------------------------
# 4/9 Install WireGuard and generate keys
# -------------------------------
echo "[4/9] Install WireGuard and generate keys"
run "apt-get update && apt-get install -y wireguard"
run "mkdir -p $WG_DIR"
run "wg genkey | tee $WG_DIR/private.key | wg pubkey | tee $WG_DIR/public.key"
check "WireGuard installed and keys generated"
pause

# -------------------------------
# 4.5/9 Configure worker wg0
# -------------------------------
echo "[4.5/9] Configure WireGuard for worker"

run "cat <<EOF > $WG_CONF
[Interface]
Address = $WORKER_VPN_IP/24
PrivateKey = $(cat $WG_DIR/private.key)

[Peer]
PublicKey = $CONTROL_PUBLIC_KEY
AllowedIps = 10.100.0.0/24
Endpoint = $CONTROL_PLANE_IP:51820
PersistentKeepalive = 25
EOF"

check "WireGuard wg0.conf created for worker $WORKER_VPN_IP"
pause

# -------------------------------
# 5/9 Start WireGuard interface
# -------------------------------
echo "[5/9] Start WireGuard interface wg0"
run "systemctl enable wg-quick@wg0"
run "systemctl start wg-quick@wg0"
check "WireGuard wg0 started"
pause

# -------------------------------
# 6/9 Install containerd
# -------------------------------
echo "[6/9] Install containerd v2.2.0"
run "wget -qO- https://github.com/containerd/containerd/releases/download/v2.2.0/containerd-2.2.0-linux-amd64.tar.gz | tar -C /usr/local -xz"
run "mkdir -p /usr/local/lib/systemd/system"
run "curl -fsSL https://raw.githubusercontent.com/containerd/containerd/main/containerd.service -o /usr/local/lib/systemd/system/containerd.service"
run "systemctl daemon-reload"
run "systemctl enable --now containerd"
check "containerd installed"
pause

echo "[6.5/9] Configure containerd (systemd cgroups)"
run "mkdir -p /etc/containerd"
run "containerd config default > /etc/containerd/config.toml"
run "sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml"
run "systemctl restart containerd"
check "containerd configured"
pause

# -------------------------------
# 7/9 Install runc and CNI
# -------------------------------
echo "[7/9] Install runc and CNI plugins"
run "wget -q https://github.com/opencontainers/runc/releases/download/v1.4.0/runc.amd64"
run "install -m 755 runc.amd64 /usr/local/sbin/runc"
run "mkdir -p /opt/cni/bin"
run "wget -q https://github.com/containernetworking/plugins/releases/download/v1.9.0/cni-plugins-linux-amd64-v1.9.0.tgz"
run "tar -C /opt/cni/bin -xzvf cni-plugins-linux-amd64-v1.9.0.tgz"
check "runc & CNI installed"
pause

# -------------------------------
# 8/9 Install Kubernetes packages
# -------------------------------
echo "[8/9] Install Kubernetes packages"
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

# -------------------------------
# 9/9 Configure kubelet
# -------------------------------
echo "[9/9] Configure kubelet (enp7s0)"
PRIVATE_IP=$(ip -4 addr show enp7s0 | grep -oP '(?<=inet\s)\d+(\.\d+){3}' || true)

if [[ -z "$PRIVATE_IP" ]]; then
  echo "❌ Failed to detect IP on enp7s0"
  exit 1
fi

run "cat <<EOF > /var/lib/kubelet/kubeadm-flags.env
KUBELET_KUBEADM_ARGS=\"--node-ip=${PRIVATE_IP}\"
EOF"
check "kubelet will advertise IP: $PRIVATE_IP"
pause

# -------------------------------
# Enable kubelet
# -------------------------------
run "systemctl daemon-reexec"
run "systemctl enable kubelet"
check "Worker ready for kubeadm join"

if $DRY_RUN; then
  echo
  echo "ℹ DRY-RUN complete — no changes were made."
fi
