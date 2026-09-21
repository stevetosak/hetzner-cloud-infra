output "network_id" {
  value       = hcloud_network.cluster.id
  description = "Provided for humans and scripts. Other modules resolve the network by name, not from this output (ADR 0002)."
}

output "network_name" {
  value = hcloud_network.cluster.name
}

output "cp_firewall_name" {
  value = hcloud_firewall.cp.name
}

output "worker_firewall_name" {
  value = hcloud_firewall.worker.name
}

output "ssh_key_name" {
  value = hcloud_ssh_key.cluster.name
}

output "cp_primary_ip_name" {
  value = hcloud_primary_ip.cp.name
}

output "cp_primary_ip_address" {
  value = hcloud_primary_ip.cp.ip_address
}

output "admin_ssh_ip" {
  value       = local.admin_ssh_ip
  description = "The address port 22 is pinned to while either allow_public_ssh_cp or allow_public_ssh_worker is true."
}
