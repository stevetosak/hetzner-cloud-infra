variable "HCLOUD_TOKEN" {
  type        = string
  sensitive   = true
  description = "Hetzner Cloud API token. Supplied as TF_VAR_HCLOUD_TOKEN from infra/.envrc."
}

variable "image" {
  type        = string
  default     = "ubuntu-24.04"
  description = "Base image for every worker."
}

variable "location" {
  type        = string
  default     = "hel1"
  description = "Hetzner location. Hetzner Volumes are location-bound, so workers must sit where the PostgreSQL volumes are (ADR 0005)."
}

variable "workers" {
  type = map(object({
    private_ip  = string
    vpn_ip      = string
    server_type = string
    labels      = map(string)
  }))
  description = <<-EOT
    The worker set, keyed by server name. The key IS the Hetzner server name
    and the Kubernetes node name, and it is stable: never derive it from a
    timestamp, a random id or any other per-run value.

    Each private_ip must be unique and inside the worker subnet 10.0.2.0/24.

    vpn_ip is the worker's address on the WireGuard VPN. Terraform creates no
    WireGuard interface, so this is a declaration Terraform carries rather than
    a resource it manages — the bootstrap reads it, BY NAME, through the
    worker_vpn_ips output.

    It is declared here because the previous bootstrap assigned VPN addresses by
    counting lines in a generated file, so a worker's address depended on its
    position in that file rather than on its identity. That is the same defect
    as the deleted node_suffix: identity must be written down, never derived
    per run.
  EOT

  validation {
    condition     = alltrue([for name in keys(var.workers) : can(regex("^[a-z0-9]([a-z0-9-]*[a-z0-9])?$", name))])
    error_message = "Worker names must be valid DNS labels; they become Kubernetes node names."
  }

  validation {
    condition     = length(distinct([for w in var.workers : w.private_ip])) == length(var.workers)
    error_message = "Each worker needs its own private_ip."
  }

  validation {
    condition     = alltrue([for w in var.workers : can(regex("^10\\.0\\.2\\.[0-9]{1,3}$", w.private_ip))])
    error_message = "Worker private_ip must be inside the worker subnet 10.0.2.0/24."
  }

  validation {
    condition     = length(distinct([for w in var.workers : w.vpn_ip])) == length(var.workers)
    error_message = "Each worker needs its own vpn_ip."
  }

  validation {
    condition     = alltrue([for w in var.workers : can(regex("^10\\.100\\.0\\.[0-9]{1,3}$", w.vpn_ip))])
    error_message = "Worker vpn_ip must be inside the VPN subnet 10.100.0.0/24."
  }

  validation {
    condition     = alltrue([for w in var.workers : w.vpn_ip != "10.100.0.1"])
    error_message = "10.100.0.1 is the control plane, the VPN hub. A worker cannot take it."
  }
}
