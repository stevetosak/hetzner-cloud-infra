resource "hcloud_server" "authos_control_plane" {
  name        = "k8s-cp"
  server_type = "cx23"
  image       = "ubuntu-24.04"
  location    = "hel1"
  ssh_keys = [data.hcloud_ssh_key.authos-cluster.id]
  firewall_ids = [hcloud_firewall.authos_cluster_firewall.id]

  network {
    network_id = hcloud_network.authos_network.id
    ip         = "10.0.1.5"
  }

  public_net {
    ipv6_enabled = false
    ipv4         = data.hcloud_primary_ip.cp_authos_ip.id
  }


  depends_on = [
    hcloud_network_subnet.cp_authos_subnet // morat vaka deka probvit paralelno da kreirat subnet i server
  ]
}

resource "hcloud_server" "workers" {
  for_each = var.workers
  name        = each.key
  server_type = each.value.server_type
  location    = "hel1"
  image       = "ubuntu-24.04"
  ssh_keys = [data.hcloud_ssh_key.authos-cluster.id]
  firewall_ids = [hcloud_firewall.authos_cluster_firewall.id]

  network {
    network_id = hcloud_network.authos_network.id
    ip         = each.value.private_ip
  }

  public_net {
    ipv4_enabled = true
    ipv6_enabled = false
  }

  depends_on = [
    hcloud_network_subnet.worker_authos_subnet
  ]

}
//todo

# resource "hcloud_load_balancer" "authos_lb" {
#   load_balancer_type = "lb11"
#   name               = "authos-lb"
#   location           = "hel1"
#   algorithm {
#     type = "round_robin"
#   }
# }

