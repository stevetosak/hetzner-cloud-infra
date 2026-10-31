---
paths:
  - "infra/**"
  - "**/*.tf"
---
# Hetzner Cloud

- A Hetzner firewall cannot filter traffic on a private network, and it attaches to servers, not to load balancers.
- The terraform provider `hcloud` 1.69.0 cannot take a firewall from one rule to none: it reports success and changes nothing. Read the firewall back from the API after such a change.
