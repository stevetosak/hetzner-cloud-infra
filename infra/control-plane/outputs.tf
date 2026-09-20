output "public_ip" {
  value       = hcloud_server.control_plane.ipv4_address
  description = "46.62.209.249 — the SSH and WireGuard endpoint."
}

output "private_ip" {
  value       = var.private_ip
  description = "The apiserver advertise address."
}

output "name" {
  value = hcloud_server.control_plane.name
}

output "id" {
  value = hcloud_server.control_plane.id
}
