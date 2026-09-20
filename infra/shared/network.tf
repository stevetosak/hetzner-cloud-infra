# The network and its four subnets survived the 2026-09-13 loss. They are
# imported, never recreated. See import.sh.

resource "hcloud_network" "authos_network" {
  name     = "authos-net"
  ip_range = "10.0.0.0/16"

  # Every other module finds this network by name. Renaming it breaks them all
  # at plan time (ADR 0002: names are interface).
  lifecycle {
    prevent_destroy = true
  }
}

resource "hcloud_network_subnet" "cp_authos_subnet" {
  network_id   = hcloud_network.authos_network.id
  type         = "cloud"
  network_zone = "eu-central"
  ip_range     = "10.0.1.0/24"
}

resource "hcloud_network_subnet" "worker_authos_subnet" {
  network_id   = hcloud_network.authos_network.id
  type         = "cloud"
  network_zone = "eu-central"
  ip_range     = "10.0.2.0/24"
}

resource "hcloud_network_subnet" "db_authos_subnet" {
  network_id   = hcloud_network.authos_network.id
  type         = "cloud"
  network_zone = "eu-central"
  ip_range     = "10.0.3.0/24"
}

resource "hcloud_network_subnet" "lb_authos_subnet" {
  network_id   = hcloud_network.authos_network.id
  type         = "cloud"
  network_zone = "eu-central"
  ip_range     = "10.0.4.0/24"
}
