# Hetzner Cloud Infrastructure + K8S

## Infrastructure

The main idea was to create a cost effective HA k8s cluster for my personal projects. Hetzner is very cheap, this whole set up costs about ~20 eur a month which include 4 [cx23](https://www.hetzner.com/cloud) servers and a Load Balancer.
The servers are configured so 1 server is the control plane while the other 3 are worker nodes. The worker node's configuration is automated via scripts, so the nodes can be conisdered to be ephemeral.

I use Terraform to manage and automate my infrastructure components.

### Networking

The nodes communicate via a private network in Hetzner. External access is done through SSH + VPN. I have Wireguard set up as vpn on the nodes, with the worker nodes + my local pc as peers. All wireguard vpn traffic goes through the control plane which acts as a proxy. I have multiple subnets to ensure some isolation and ease of networking rules if needed. The control plane, workers, and load balancer each reside in a seperate subnet.


## Kuberenetes
### Installation and Resources
- Kuberentes is managed and installed using [kubeadm](https://kubernetes.io/docs/reference/setup-tools/kubeadm/). 
- Container runtime for kubernetes used is [containerd](https://containerd.io/). 
- Storage is managed by [Longhorn](https://longhorn.io/) which provisions volumes from my nodes local storage.
- Database currently used is Postgresql, administered with [CloudNativePG](https://cloudnative-pg.io/docs/1.28/)
- TLS certificates are managed by [cert-manager](https://cert-manager.io/). (Certificates are issued by LetsEncrypt)

### Automation scripts
`infra/scripts/bootsrap` provides a set of bootstrap scripts which configure each node with all the preqrequistes to run workloads on the cluster, in a fully automated way. The scripts handle automated vpn set up, containerd installation, kubernetes installation, longhorn installation for each worker node. `infra/scripts/bootstrap/reset-nodes/sh` handles destroying and re-creating the worker nodes, which can be set to be a variable number. This serves as a hard reset for the workers. Because the node setup is automated, i can save costs by bringing down a worker node (or all worker nodes) and provision them when needed. When reseting the workers, the next `terraform apply` is run with the var `allow_public_ssh=true`, which sets a dynamic rule in the Hetzner cloud firewall to allow incoming ssh connections to nodes but only from my local pc's public ip.
The bootstrap script `infra/scripts/bootstrap/bootstrap_workers.sh` is the main entrypoint. It sets up the vpn, tests connectivity and runs each modular bootstrap script on each worker node.`

### Issues with ephemeral nodes

An issue which arised was longhorn disks would degrade and not be usable/schedulable after each reset of the nodes. After much digging, the issue was due to mismatching Disk UUID's. In essence, when a worker node would get recreated, i was assigning the same name to the worker. That name is referenced by longhorn internally and is bound to the Disk UUID. But when a new node gets created, Hetzner provisions a new disk with a new UUID. So while this is a new node, having the old name meant that longhorn was still referencing the old disk, but a new disk was provisioned, leading to this error. The fix was to make the node names unique, so every time they are recreated they have a unique name. The easiest way to do this is with a timestamp.
