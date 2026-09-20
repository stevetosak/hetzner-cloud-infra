data "http" "local_pub_ip" {
  url = "https://ipv4.icanhazip.com"
}

locals {
  admin_ssh_ip = "${chomp(data.http.local_pub_ip.response_body)}/32"
}

resource "hcloud_firewall" "authos_cluster_firewall" {
  name = "authos-cluster-firewall"

  # WireGuard. This is the only port open to the world.
  rule {
    direction  = "in"
    protocol   = "udp"
    port       = "51820"
    source_ips = ["0.0.0.0/0"]
  }

  # Bootstrap-only SSH, pinned to one address. See var.allow_public_ssh.
  dynamic "rule" {
    for_each = var.allow_public_ssh ? [1] : []
    content {
      direction  = "in"
      protocol   = "tcp"
      port       = "22"
      source_ips = [local.admin_ssh_ip]
    }
  }

  # apply_to is deliberately unset. Servers attach themselves through
  # firewall_ids in the control-plane and workers modules, so this module never
  # needs to name a server.
}
