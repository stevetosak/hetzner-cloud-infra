workers = {
  k8swk1 = {
    private_ip  = "10.0.2.6"
    server_type = "cx23"
    labels = {
      role = "worker"
    }
  }
  k8swk2 = {
    private_ip  = "10.0.2.7"
    server_type = "cx23"
    labels = {
      role = "worker"
    }
  }
  k8swk3 = {
    private_ip  = "10.0.2.8"
    server_type = "cx23"
    labels = {
      role = "worker"
    }
  }
}
