#!/usr/bin/env bash
#
# Phase 1 — seed shared/ state from the resources that survived the
# 2026-09-13 loss. These exist in Hetzner today; creating them would be wrong.
#
# `terraform import` is a state-only operation. It reads the Hetzner API and
# writes the result into the state file. It never creates, changes or deletes
# anything in Hetzner. Re-running this script is safe: addresses already in
# state are skipped.
#
# Prerequisites:
#   TF_VAR_HCLOUD_TOKEN    from infra/.envrc
#   AWS_ACCESS_KEY_ID      R2 access key id
#   AWS_SECRET_ACCESS_KEY  R2 secret access key
#   terraform init         already run against the R2 backend
#
set -euo pipefail
cd "$(dirname "$0")"

NETWORK_ID=11736362
FIREWALL_ID=10289761
SSH_KEY_ID=104278281
PRIMARY_IP_ID=110296483

IN_STATE="$(terraform state list 2>/dev/null || true)"

import() {
  local address="$1" id="$2"
  if printf '%s\n' "$IN_STATE" | grep -qxF "$address"; then
    echo "skip    $address (already in state)"
    return
  fi
  echo "import  $address <- $id"
  terraform import "$address" "$id"
}

import hcloud_network.authos_network "$NETWORK_ID"

# A subnet's import id is "<network id>-<ip range>".
import hcloud_network_subnet.cp_authos_subnet     "${NETWORK_ID}-10.0.1.0/24"
import hcloud_network_subnet.worker_authos_subnet "${NETWORK_ID}-10.0.2.0/24"
import hcloud_network_subnet.db_authos_subnet     "${NETWORK_ID}-10.0.3.0/24"
import hcloud_network_subnet.lb_authos_subnet     "${NETWORK_ID}-10.0.4.0/24"

import hcloud_firewall.authos_cluster_firewall "$FIREWALL_ID"
import hcloud_ssh_key.authos_cluster           "$SSH_KEY_ID"
import hcloud_primary_ip.cp_authos_ip          "$PRIMARY_IP_ID"

echo
echo "Done. Now verify with:  terraform plan"
echo "Expect zero create, zero destroy, zero replace. See infra/README.md."
