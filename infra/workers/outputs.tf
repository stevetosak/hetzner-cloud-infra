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
