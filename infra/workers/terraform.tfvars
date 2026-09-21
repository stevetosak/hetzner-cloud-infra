workers = {
  k8swk1 = {
    private_ip  = "10.0.2.6"
    vpn_ip      = "10.100.0.2"
    server_type = "cx23"
    labels = {
      role = "worker"
    }
  }
  k8swk2 = {
    private_ip  = "10.0.2.7"
    vpn_ip      = "10.100.0.3"
    server_type = "cx23"
    labels = {
      role = "worker"
    }
  }
  k8swk3 = {
    private_ip  = "10.0.2.8"
    vpn_ip      = "10.100.0.4"
    server_type = "cx23"
    labels = {
      role = "worker"
    }
  }
}
