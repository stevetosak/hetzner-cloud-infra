resource "hcloud_network" "authos_network" {
  ip_range = "10.0.0.0/16"
  name     = "authos-net"
}

resource "hcloud_network_subnet" "cp_authos_subnet" {
  network_id = hcloud_network.authos_network.id
  type = "cloud"
  network_zone = "eu-central"
  ip_range = "10.0.1.0/24"
}

resource "hcloud_network_subnet" "worker_authos_subnet" {
  network_id = hcloud_network.authos_network.id
  type = "cloud"
  network_zone = "eu-central"
  ip_range = "10.0.2.0/24"
}
resource "hcloud_network_subnet" "db_authos_subnet" {
  network_id = hcloud_network.authos_network.id
  type = "cloud"
  network_zone = "eu-central"
  ip_range = "10.0.3.0/24"
}

//todo
#
# resource "hcloud_load_balancer_network" "authos_lb_network" {
#   load_balancer_id = hcloud_load_balancer.authos_lb.id
#   subnet_id = hcloud_network_subnet.worker_authos_subnet.id
#   ip = "10.0.2.10"
# }

resource "hcloud_firewall" "authos_cluster_firewall" {
  name = "authos-cluster-firewall"
  rule {
    direction = "in"
    protocol  = "udp"
    port = "51820"
    source_ips = [
      "0.0.0.0/0"
    ]
  }
  dynamic "rule" {
    for_each = var.allow_public_ssh ? [1] : []
    content {
      direction  = "in"
      protocol   = "tcp"
      port       = "22"
      source_ips = [local.admin_ssh_ip]
    }
  }
}



# resource "hcloud_primary_ip" "cp_authos_ip" {
#   assignee_type = "server"
#   auto_delete   = false
#   type          = "ipv4"
#   name = "cp-authos-ip"
#   datacenter = "hel1-dc2"
#
#    lifecycle {prevent_destroy = true}
# }
# resource "hcloud_primary_ip" "wk_authos_ips" {
#   count = var.worker_count
#   datacenter = "hel1-dc2"
#   name = "wk-authos-${count.index + 1}-ip"
#   assignee_type = "server"
#   auto_delete   = false
#   type          = "ipv4"
#
#    lifecycle {prevent_destroy = true}
# }




