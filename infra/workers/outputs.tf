output "worker_names" {
  value       = sort([for s in hcloud_server.workers : s.name])
  description = "Stable names: k8swk1, k8swk2, k8swk3."
}

output "worker_public_ips" {
  value = { for k, s in hcloud_server.workers : k => s.ipv4_address }
}

output "worker_private_ips" {
  value = { for k, s in hcloud_server.workers : k => one(s.network).ip }
}

# The bootstrap reads this BY NAME. A worker's VPN address is declared in
# terraform.tfvars, never derived from the order of a generated file.
output "worker_vpn_ips" {
  value       = { for k, w in var.workers : k => w.vpn_ip }
  description = "WireGuard address per worker name. 10.100.0.1 is the control plane hub."
}
