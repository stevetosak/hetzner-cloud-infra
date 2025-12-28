output "worker_public_ips" {
  value = [for s in hcloud_server.workers : s.ipv4_address]
}