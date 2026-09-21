# The workers, and nothing else. This module holds no reference to the control
# plane and cannot express one, so no worker operation can plan against it
# (ADR 0002).
#
# Worker names are STABLE: k8swk1, k8swk2, k8swk3, forever.
#
# There is no var.node_suffix and no per-worker name_suffix. The only reason
# names ever had to change was Longhorn binding a node name to a local disk
# UUID, and Longhorn is gone (ADR 0005). A global suffix minted per CLI
# invocation renamed every worker at once, and a name change forces server
# replacement — that is what destroyed every database on 2026-09-13.
#
# A name that never changes cannot force a replacement. Do not reintroduce a
# suffix in any form.

resource "hcloud_server" "workers" {
  for_each = var.workers

  name        = each.key
  server_type = each.value.server_type
  image       = var.image
  location    = var.location

  ssh_keys     = [data.hcloud_ssh_key.cluster.id]
  firewall_ids = [data.hcloud_firewall.worker.id]

  network {
    network_id = data.hcloud_network.cluster.id
    ip         = each.value.private_ip
  }

  public_net {
    ipv4_enabled = true
    ipv6_enabled = false
  }

  labels = each.value.labels

  # No create_before_destroy. Hetzner requires server names to be unique among
  # live servers, so a replacement must delete the old server first (ADR 0005).
  #
  # Because names are now reused, a recreated worker inherits nothing from its
  # predecessor in Hetzner but everything in Kubernetes: delete the old Node
  # object before the new server joins, or it picks up stale taints and labels.
}
