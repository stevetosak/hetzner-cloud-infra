# The network and its four subnets survived the 2026-09-13 loss. They are
# imported, never recreated. See import.sh.

resource "hcloud_network" "cluster" {
  name     = "tosak-net"
  ip_range = "10.0.0.0/16"

  # Every other module finds this network by name. Renaming it breaks them all
  # at plan time (ADR 0002: names are interface).
  lifecycle {
    prevent_destroy = true
  }
}

resource "hcloud_network_subnet" "cp" {
  network_id   = hcloud_network.cluster.id
  type         = "cloud"
  network_zone = "eu-central"
  ip_range     = "10.0.1.0/24"
}

resource "hcloud_network_subnet" "worker" {
  network_id   = hcloud_network.cluster.id
  type         = "cloud"
  network_zone = "eu-central"
  ip_range     = "10.0.2.0/24"
}

# Reserved. Holds no server today and cannot, under the current design:
# PostgreSQL is a pod on a Worker with a Hetzner Volume attached (ADR 0005), so
# it takes a pod address, never a subnet address. The range is held for a future
# core server that is NOT part of the Kubernetes cluster — a dedicated database
# host, or a managed Hetzner database.
#
# Do not read this subnet as a security boundary. Hetzner Cloud Firewalls filter
# the public interface only and cannot express a rule about private traffic at
# all; their own FAQ says so. Subnets here buy readable addressing, so that a
# host-level nftables rule can be written against a role instead of a list of
# addresses. See ADR 0006.
resource "hcloud_network_subnet" "reserved" {
  network_id   = hcloud_network.cluster.id
  type         = "cloud"
  network_zone = "eu-central"
  ip_range     = "10.0.3.0/24"
}


resource "hcloud_network_subnet" "lb" {
  network_id   = hcloud_network.cluster.id
  type         = "cloud"
  network_zone = "eu-central"
  ip_range     = "10.0.4.0/24"
}

# Resource labels were renamed when the shared infrastructure stopped being
# named after one of the applications it hosts (ADR 0007). A moved block is a
# state move: nothing in Hetzner is destroyed or recreated. The third subnet
# was additionally called db_authos_subnet, from when the range was expected to
# hold a database server.
moved {
  from = hcloud_network.authos_network
  to   = hcloud_network.cluster
}

moved {
  from = hcloud_network_subnet.cp_authos_subnet
  to   = hcloud_network_subnet.cp
}

moved {
  from = hcloud_network_subnet.worker_authos_subnet
  to   = hcloud_network_subnet.worker
}

moved {
  from = hcloud_network_subnet.db_authos_subnet
  to   = hcloud_network_subnet.reserved
}

moved {
  from = hcloud_network_subnet.lb_authos_subnet
  to   = hcloud_network_subnet.lb
}
