variable "HCLOUD_TOKEN" {
  type        = string
  sensitive   = true
  description = "Hetzner Cloud API token. Supplied as TF_VAR_HCLOUD_TOKEN from infra/.envrc."
}

variable "server_type" {
  type        = string
  default     = "cx23"
  description = "Hetzner server type for the control plane."
}

variable "image" {
  type        = string
  default     = "ubuntu-24.04"
  description = "Base image. Changing this forces a rebuild, which rebuild_protection refuses until it is cleared by hand."
}

variable "location" {
  type        = string
  default     = "hel1"
  description = "Hetzner location. Must match the location of the Hetzner Volumes used by PostgreSQL (ADR 0005)."
}

variable "private_ip" {
  type        = string
  default     = "10.0.1.5"
  description = <<-EOT
    Address inside the cp subnet 10.0.1.0/24. This is the apiserver advertise
    address, so it is baked into every kubeadm certificate and every node's
    kubeconfig. Changing it means re-issuing the control-plane certificates.
  EOT
}
