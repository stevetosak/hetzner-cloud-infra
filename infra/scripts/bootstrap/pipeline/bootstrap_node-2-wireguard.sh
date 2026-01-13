#!/bin/bash
set -e

INTERACTIVE=false
DRY_RUN=false
WG_DIR="/etc/wireguard"
WG_CONF="$WG_DIR/wg0.conf"

for arg in "$@"; do
  case "$arg" in
    --interactive) INTERACTIVE=true ;;
    --dry-run) DRY_RUN=true ;;
  esac
done

source "/tmp/bootstrap_utils.sh"

CONTROL_PUBLIC_KEY="$1"
WORKER_VPN_IP="$2"
CONTROL_PLANE_IP="$3"

if [[ -z "$CONTROL_PUBLIC_KEY" || -z "$WORKER_VPN_IP" || -z "$CONTROL_PLANE_IP" ]]; then
    echo "❌ Missing required arguments"
    exit 1
fi

echo "[1/3] Install WireGuard and generate keys"
run "apt-get update && apt-get install -y wireguard"
run "mkdir -p $WG_DIR"
run "wg genkey | tee $WG_DIR/private.key | wg pubkey | tee $WG_DIR/public.key"
check "WireGuard installed and keys generated"
pause

echo "[2/3] Configure wg0.conf"
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
check "WireGuard wg0.conf created"
pause

echo "[3/3] Start WireGuard interface"
run "systemctl enable wg-quick@wg0"
run "systemctl start wg-quick@wg0"
check "WireGuard started"
pause
