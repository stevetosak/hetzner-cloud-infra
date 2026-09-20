data "http" "local_pub_ip" {
  url = "https://ipv4.icanhazip.com"
}

locals {
  admin_ssh_ip = "${chomp(data.http.local_pub_ip.response_body)}/32"
}

# Two firewalls, because the VPN is hub and spoke and only the hub listens.
#
# A Worker's wg0 has an Endpoint and no ListenPort: it dials out from an
# ephemeral port. The Control Plane has a ListenPort and peers with no Endpoint,
# so it learns where each peer is from the first packet that arrives, and
# PersistentKeepalive keeps that mapping fresh. A Worker therefore never needs
# an inbound WireGuard port, and one firewall covering every server would have
# opened UDP 51820 to the world on four machines to serve one (ADR 0006).
#
# Hetzner firewalls are stateful, so a Worker with no inbound rules still
# reaches apt and the image registries, and the replies come back.
#
# apply_to is deliberately unset on both. Servers attach themselves through
# firewall_ids in the control-plane and workers modules, so this module never
# names a server (ADR 0002).

resource "hcloud_firewall" "cp" {
  name = "tosak-cp-firewall"

  # WireGuard. The only port open to the world anywhere in this cluster.
  rule {
    direction  = "in"
    protocol   = "udp"
    port       = "51820"
    source_ips = ["0.0.0.0/0"]
  }

  dynamic "rule" {
    for_each = var.allow_public_ssh_cp ? [1] : []
    content {
      direction  = "in"
      protocol   = "tcp"
      port       = "22"
      source_ips = [local.admin_ssh_ip]
    }
  }
}

resource "hcloud_firewall" "worker" {
  name = "tosak-worker-firewall"

  # Bootstrap SSH and nothing else. With allow_public_ssh_worker false this
  # firewall holds no inbound rule at all, which is the intended steady state:
  # a Worker accepts nothing on its public interface. Operators reach it over
  # the VPN, through the Control Plane.
  dynamic "rule" {
    for_each = var.allow_public_ssh_worker ? [1] : []
    content {
      direction  = "in"
      protocol   = "tcp"
      port       = "22"
      source_ips = [local.admin_ssh_ip]
    }
  }
}

# The imported firewall (ID 10289761) becomes the control-plane one. The worker
# firewall is new — a create, made while no server exists to be affected.
moved {
  from = hcloud_firewall.authos_cluster_firewall
  to   = hcloud_firewall.cp
}
