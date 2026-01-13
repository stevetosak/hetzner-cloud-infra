variable "HCLOUD_TOKEN" {
  sensitive = true
}
variable "worker_count" {
  type        = number
  nullable    = false
  default     = 2
  description = "Number of worker servers to create"
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





