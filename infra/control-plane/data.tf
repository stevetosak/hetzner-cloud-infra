# Shared resources are resolved by name, not through terraform_remote_state.
# This module therefore needs no access to the shared state or its backend
# credentials, and cannot modify anything shared/ owns (ADR 0002).
#
# Apply order is shared/ first. If these lookups fail, shared/ has not been
# applied, or a name was changed.

data "hcloud_network" "cluster" {
  name = "tosak-net"
}

data "hcloud_firewall" "cp" {
  name = "tosak-cp-firewall"
}

data "hcloud_ssh_key" "cluster" {
  name = "tosak-cluster"
}

data "hcloud_primary_ip" "cp" {
  name = "tosak-cp-ip"
}
