data "hcloud_ssh_key" "authos-cluster" {
  name = "authos-cluster"
}

data "hcloud_primary_ip" "cp_authos_ip" {
  name = "cp-authos-ip"
}
data "http" "local_pub_ip" {
  url = "https://ipv4.icanhazip.com"
}

locals {
  admin_ssh_ip = "${chomp(data.http.local_pub_ip.response_body)}/32"
}