# The control plane, and nothing else. This module cannot express a worker.
#
# Four guards stand between any operation here and the loss of 2026-09-13
# (ADR 0002). Three of them are on this resource:
#
#   lifecycle.prevent_destroy   Terraform refuses to plan a destroy.
#   delete_protection           Hetzner refuses a delete_server call.
#   rebuild_protection          Hetzner refuses a rebuild_server call.
#
# The last two are enforced by the Hetzner API, so they also hold against a
# stale state file, a rogue token, and a CLI bug. Every delete_server call in
# the incident log would have been refused.
#
# To replace the control plane deliberately, see "Replacing the control plane"
# in infra/README.md. The friction is the feature; it is not a bug to route
# around.

resource "hcloud_server" "control_plane" {
  name        = "k8s-cp"
  server_type = var.server_type
  image       = var.image
  location    = var.location

  ssh_keys     = [data.hcloud_ssh_key.cluster.id]
  firewall_ids = [data.hcloud_firewall.cp.id]

  delete_protection  = true
  rebuild_protection = true

  network {
    network_id = data.hcloud_network.cluster.id
    ip         = var.private_ip
  }

  public_net {
    ipv4_enabled = true
    ipv6_enabled = false

    # Reuse the surviving primary IP 46.62.209.249, so the SSH and WireGuard
    # endpoint is unchanged from before the loss.
    ipv4 = data.hcloud_primary_ip.cp.id
  }

  # The SSH host key, seeded through cloud-init so the first login can be
  # verified (ADR 0009). kluster passes it only when it creates the server.
  user_data = var.user_data

  labels = {
    role = "control-plane"
  }

  lifecycle {
    prevent_destroy = true

    # user_data is ForceNew. Without this, adding it to the live server, or a
    # new key on the next run, would plan a replacement.
    ignore_changes = [user_data]
  }
}
