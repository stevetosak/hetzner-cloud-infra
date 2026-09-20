variable "HCLOUD_TOKEN" {
  type        = string
  sensitive   = true
  description = "Hetzner Cloud API token. Supplied as TF_VAR_HCLOUD_TOKEN from infra/.envrc."
}

# Port 22 is gated per firewall, not once for the whole cluster. The two
# machines reach their bootstrap SSH at different times: the Control Plane
# needs it before its WireGuard hub exists (Phase 2), each Worker needs it
# before that Worker holds a tunnel (Phase 3). A single switch would have
# reopened the Control Plane every time a Worker was built, which is the habit
# ADR 0004 was written to break.
#
# Both default to false. That is the steady state: no server accepts anything
# on its public interface except the hub's WireGuard port.

variable "allow_public_ssh_cp" {
  type        = bool
  default     = false
  description = <<-EOT
    Open port 22 on tosak-cp-firewall to the admin workstation's current public
    IP. Set true only while bootstrapping the Control Plane; close it again
    once WireGuard is up (ADR 0004). The source address is resolved at plan
    time from the machine running terraform, so a changed ISP address shows up
    as an in-place firewall update. That is the mechanism working, not drift.
  EOT
}

variable "allow_public_ssh_worker" {
  type        = bool
  default     = false
  description = <<-EOT
    Open port 22 on tosak-worker-firewall to the admin workstation's current
    public IP. Set true only while bootstrapping a Worker, whose first SSH must
    use the public address because its tunnel does not exist yet; close it
    again once that Worker is peered on the hub (ADR 0004). Same plan-time
    address resolution as allow_public_ssh_cp.
  EOT
}
