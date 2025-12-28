variable "HCLOUD_TOKEN" {
  sensitive = true
}
variable "worker_count" {
  type        = number
  nullable    = false
  default     = 2
  description = "Number of worker servers to create"
}

locals {
  workers = {
    for i in range(var.worker_count) :
    "k8s-wk${i + 1}" => {
      ip          = "10.0.2.${6 + i}"
      server_type = "cx23"
      labels = {
        role = "worker"
      }
    }
  }
}

variable "workers" {
  type = map(object({
    private_ip = string
    server_type = string
    labels      = map(string)
  }))
  description = "Template for creating worker nodes. The private ip must differ, and the server type can differ"
}

variable "allow_public_ssh" {
  type = bool
  default = false
}





