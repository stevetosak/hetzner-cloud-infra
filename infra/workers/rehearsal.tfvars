# The rehearsal Worker set, for the tosak-rehearsal project only (ADR 0009).
#
# kluster passes this file with -var-file under --env rehearsal and never
# under live. terraform.tfvars loads in every environment, and this file wins
# over it, so a rehearsal `node add` writes here and never into the live set.
#
# It declares the same Workers as live and stays committed between rehearsals:
# `kluster up` builds what it declares and never writes it (ADR 0009,
# Amendment 2026-10-25). `kluster node add` and `node remove` edit it; commit
# each edit before the next run, or kluster refuses to plan.
workers = {
  k8swk1 = {
    labels = {
      role = "worker"
    }
    private_ip  = "10.0.2.6"
    server_type = "cx23"
    vpn_ip      = "10.100.0.2"
  }
  k8swk2 = {
    labels = {
      role = "worker"
    }
    private_ip  = "10.0.2.7"
    server_type = "cx23"
    vpn_ip      = "10.100.0.3"
  }
  k8swk3 = {
    labels = {
      role = "worker"
    }
    private_ip  = "10.0.2.8"
    server_type = "cx23"
    vpn_ip      = "10.100.0.4"
  }
}
