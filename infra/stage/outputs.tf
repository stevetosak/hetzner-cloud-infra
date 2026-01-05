output "worker_public_ips" {
  value = [for s in hcloud_server.workers : s.ipv4_address]
}
output "worker_names" {
  value = [for s in hcloud_server.workers : s.name]
}