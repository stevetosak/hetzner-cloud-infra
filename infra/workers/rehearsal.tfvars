# The rehearsal Worker set, for the tosak-rehearsal project only (ADR 0009).
#
# kluster passes this file with -var-file under --env rehearsal and never
# under live. terraform.tfvars loads in every environment, and this file wins
# over it, so a rehearsal `node add` writes here and never into the live set.
#
# `kluster node add` and `node remove` edit it; commit each edit before the
# next run, or kluster refuses to plan. Empty between rehearsals.
workers = {
}
