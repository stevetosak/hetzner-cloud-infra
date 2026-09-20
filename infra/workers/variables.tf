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
    server_type = string
    labels      = map(string)
  }))
  description = <<-EOT
    The worker set, keyed by server name. The key IS the Hetzner server name
    and the Kubernetes node name, and it is stable: never derive it from a
    timestamp, a random id or any other per-run value.

    Each private_ip must be unique and inside the worker subnet 10.0.2.0/24.
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
}
