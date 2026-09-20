# The ssh key and the control-plane primary IP were previously read as data
# sources, so the repository could not have rebuilt them into an empty Hetzner
# project. They are managed here instead (ADR 0002: shared/ owns the ssh key
# and the primary IP). Both are imported, not created.

resource "hcloud_ssh_key" "cluster" {
  name       = "tosak-cluster"
  public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB+HWWsHT00arJTAEDkaWOFTFcF2zLD+5PJLg7cVYy8u"

  lifecycle {
    prevent_destroy = true
  }
}

resource "hcloud_primary_ip" "cp" {
  name     = "tosak-cp-ip"
  type     = "ipv4"
  location = "hel1"

  # Never release the address when the server it is attached to goes away.
  # 46.62.209.249 is the SSH and WireGuard endpoint; losing it means editing
  # every peer config and every DNS record that points at the control plane.
  auto_delete       = false
  delete_protection = true

  # assignee_id is deliberately unset. The control-plane module attaches this
  # IP from its own side, through public_net.ipv4. Naming a server here would
  # put a control-plane reference into the shared module.
  lifecycle {
    prevent_destroy = true
  }
}

moved {
  from = hcloud_ssh_key.authos_cluster
  to   = hcloud_ssh_key.cluster
}

moved {
  from = hcloud_primary_ip.cp_authos_ip
  to   = hcloud_primary_ip.cp
}
