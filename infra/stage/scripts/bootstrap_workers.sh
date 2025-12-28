#!/bin/bash
set -e

# -------------------------------
# Config
# -------------------------------
SCRIPT_DIR="$HOME/Projects/Authos/infra/stage/scripts/workers/pipeline"
SSH_OPTIONS="-o StrictHostKeyChecking=no"
WORKER_IPS_FILE="$HOME/Projects/Authos/infra/stage/out/worker_ips.txt"
WG_CONF="/etc/wireguard/wg0.conf"
VPN_BASE="10.100.0"

CONTROL_PLANE_WG_IP="10.100.0.1"

INTERACTIVE=false
DRY_RUN=false

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
# Validate environment
# -------------------------------
if [[ -z "$CONTROL_PLANE_IP" ]]; then
    echo "❌ CONTROL_PLANE_IP environment variable is not set"
    exit 1
fi
echo "✔ Using public control plane IP: $CONTROL_PLANE_IP"

if [[ ! -f "$WORKER_IPS_FILE" ]]; then
    echo "❌ Worker IPs file not found: $WORKER_IPS_FILE"
    exit 1
fi

# -------------------------------
# Fetch control plane WireGuard public key
# -------------------------------
CONTROL_PUBLIC_KEY=$(ssh root@"$CONTROL_PLANE_WG_IP" "cat /etc/wireguard/public.key")
if [[ -z "$CONTROL_PUBLIC_KEY" ]]; then
    echo "❌ Failed to fetch control plane WireGuard public key"
    exit 1
fi
echo "✔ Control plane WireGuard public key fetched"

# -------------------------------
# Read worker nodes
# -------------------------------
NODES=()
while IFS= read -r ip; do
    [[ -n "$ip" ]] && NODES+=("$ip")
done < "$WORKER_IPS_FILE"

if [[ ${#NODES[@]} -eq 0 ]]; then
    echo "❌ No worker nodes found in $WORKER_IPS_FILE"
    exit 1
fi
echo "✔ Found ${#NODES[@]} worker nodes"

# -------------------------------
# Assign VPN IPs
# -------------------------------
VPN_INDEX=2
declare -A WORKER_VPN_IPS
for IP in "${NODES[@]}"; do
    WORKER_VPN_IPS[$IP]="${VPN_BASE}.${VPN_INDEX}"
    VPN_INDEX=$((VPN_INDEX+1))
done

# -------------------------------
# Define the modular scripts for node bootstrap
# -------------------------------
NODE_SCRIPTS=(
    "$SCRIPT_DIR/bootstrap_node-1-user-sysctl.sh"
    "$SCRIPT_DIR/bootstrap_node-2-wireguard.sh"
    "$SCRIPT_DIR/bootstrap_node-3-containerd.sh"
    "$SCRIPT_DIR/bootstrap_node-4-kubernetes.sh"
    "$SCRIPT_DIR/bootstrap_node-5-longhorn.sh"
)

  KUBE_JOIN_COMMAND=$(ssh root@$CONTROL_PLANE_WG_IP "kubeadm token create --print-join-command")

# -------------------------------
# Function to run modular scripts on a node
# -------------------------------
bootstrap_node() {
    local NODE_IP="$1"
    local VPN_IP="$2"
    local UTILS_SCRIPT="$SCRIPT_DIR/bootstrap_utils.sh"

    # Copy bootstrap_utils.sh to the node
    echo "=== Copying bootstrap_utils.sh to $NODE_IP ==="
    scp $SSH_OPTIONS "$UTILS_SCRIPT" root@"$NODE_IP":/tmp/bootstrap_utils.sh
    echo "=== Copied bootstrap_utils.sh to $NODE_IP ==="

    # Run each modular script
    for SCRIPT in "${NODE_SCRIPTS[@]}"; do
        CMD="bash -s -- '$CONTROL_PUBLIC_KEY' '$VPN_IP' '$CONTROL_PLANE_IP' '$KUBE_JOIN_COMMAND'"
        $INTERACTIVE && CMD+=" --interactive"
        $DRY_RUN && CMD+=" --dry-run"

        echo "=== Running $SCRIPT on $NODE_IP ==="
        ssh "$SSH_OPTIONS" root@"$NODE_IP" "$CMD" < "$SCRIPT"
        echo "=== Finished $SCRIPT on $NODE_IP ==="
    done

  }
#fetch kubeadm join command




# -------------------------------
# Bootstrap all nodes in parallel -> NOT DONE IN PARALLEL
# -------------------------------
for NODE_IP in "${NODES[@]}"; do
    bootstrap_node "$NODE_IP" "${WORKER_VPN_IPS[$NODE_IP]}" &
done
wait
echo "✔ All workers bootstrapped"

# -------------------------------
# Update control plane WireGuard
# -------------------------------
echo "Updating control plane WireGuard configuration"

# Collect worker public keys
declare -A WORKER_KEYS
for NODE_IP in "${NODES[@]}"; do
    PUB_KEY=$(ssh $SSH_OPTIONS root@"$NODE_IP" "cat /etc/wireguard/public.key")
    WORKER_KEYS[$NODE_IP]=$PUB_KEY
done
echo "Updating control plane WireGuard configuration..."

# Fetch worker public keys
declare -A WORKER_KEYS
for NODE_IP in "${NODES[@]}"; do
PUB_KEY=$(ssh $SSH_OPTIONS root@"$NODE_IP" "cat /etc/wireguard/public.key")
WORKER_KEYS[$NODE_IP]=$PUB_KEY
done

    # Fetch worker public keys
declare -A WORKER_KEYS
for NODE_IP in "${NODES[@]}"; do
PUB_KEY=$(ssh $SSH_OPTIONS root@"$NODE_IP" "cat /etc/wireguard/public.key")
WORKER_KEYS[$NODE_IP]=$PUB_KEY
done

    # Collect worker public keys
echo "Fetching worker public keys..."
declare -A WORKER_KEYS
for NODE_IP in "${NODES[@]}"; do
PUB_KEY=$(ssh $SSH_OPTIONS root@"$NODE_IP" "cat /etc/wireguard/public.key")
WORKER_KEYS[$NODE_IP]=$PUB_KEY
echo "Fetched public key from $NODE_IP"
done

    # Temporary local config file
TMP_WG_CONF=$(mktemp)

echo "Fetching control plane private key..."
CONTROL_PLANE_PRIVATE_KEY=$(ssh $SSH_OPTIONS root@"$CONTROL_PLANE_WG_IP" "cat /etc/wireguard/private.key")

    # Write interface section
cat > "$TMP_WG_CONF" <<EOT
[Interface]
Address = 10.100.0.1/24
PrivateKey = $CONTROL_PLANE_PRIVATE_KEY
ListenPort = 51820
EOT

    # Add dynamic worker peers
    INDEX=2
    for NODE_IP in "${NODES[@]}"; do
        cat >> "$TMP_WG_CONF" <<EOP

# $NODE_IP
[Peer]
PublicKey = ${WORKER_KEYS[$NODE_IP]}
AllowedIPs = $VPN_BASE.${INDEX}/32
EOP
        INDEX=$((INDEX+1))
    done

    # Add static remote peer
    cat >> "$TMP_WG_CONF" <<EOR

# remote
[Peer]
PublicKey = 7v0/KwZNn08j5rtN8IQ2C8f9kOeuEqKEIlWwr4Qhq00=
AllowedIPs = 10.100.0.69/32
EOR

    # Copy the config to the control plane
    scp $SSH_OPTIONS "$TMP_WG_CONF" root@"$CONTROL_PLANE_WG_IP":/tmp/wg0.conf

    # Move into place and restart wg
    ssh $SSH_OPTIONS root@"$CONTROL_PLANE_WG_IP" "
        cp /etc/wireguard/wg0.conf /etc/wireguard/wg0.conf.backup
        mv /tmp/wg0.conf /etc/wireguard/wg0.conf
        systemctl restart wg-quick@wg0
    "

    # Cleanup local temp file
    rm -f "$TMP_WG_CONF"

    echo "✔ Control plane wg0.conf rebuilt and WireGuard restarted"



# -------------------------------
# Validate VPN connectivity
# -------------------------------
echo "Validating VPN connectivity to workers"
for NODE_IP in "${NODES[@]}"; do
    VPN_IP=${WORKER_VPN_IPS[$NODE_IP]}
    echo -n "Pinging $NODE_IP ($VPN_IP)... "
    if ping -c 2 -W 2 "$VPN_IP" &>/dev/null; then
        echo "✔ reachable"
    else
        echo "❌ unreachable"
    fi
done

echo "✅ Bootstrap complete"

echo "Installing longhorn prerequisites.."
ssh root@$CONTROL_PLANE_WG_IP "export KUBECONFIG=/etc/kubernetes/admin.conf && /root/longhornctl install preflight && /root/longhornctl check preflight"
