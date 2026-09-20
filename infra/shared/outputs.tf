output "network_id" {
  value       = hcloud_network.authos_network.id
  description = "Provided for humans and scripts. Other modules resolve the network by name, not from this output (ADR 0002)."
}

output "network_name" {
  value = hcloud_network.authos_network.name
}

output "firewall_name" {
  value = hcloud_firewall.authos_cluster_firewall.name
}

output "ssh_key_name" {
  value = hcloud_ssh_key.authos_cluster.name
}

output "cp_primary_ip_name" {
  value = hcloud_primary_ip.cp_authos_ip.name
}

output "cp_primary_ip_address" {
  value = hcloud_primary_ip.cp_authos_ip.ip_address
}

output "admin_ssh_ip" {
  value       = local.admin_ssh_ip
  description = "The address port 22 is pinned to while var.allow_public_ssh is true."
}
