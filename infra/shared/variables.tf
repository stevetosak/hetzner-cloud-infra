variable "HCLOUD_TOKEN" {
  type        = string
  sensitive   = true
  description = "Hetzner Cloud API token. Supplied as TF_VAR_HCLOUD_TOKEN from infra/.envrc."
}

variable "allow_public_ssh" {
  type        = bool
  default     = false
  description = <<-EOT
    Open port 22 on the cluster firewall to the admin workstation's current
    public IP. Set true only while bootstrapping a server; close it again once
    WireGuard is up (ADR 0004). The source address is resolved at plan time
    from the machine running terraform, so a changed ISP address shows up as an
    in-place firewall update. That is the mechanism working, not drift.
  EOT
}
