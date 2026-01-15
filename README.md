# Hetzner Cloud Infrastructure + K8S

## Infrastructure

The main idea was to create a cost effective HA k8s cluster for my personal projects. Hetzner is very cheap, this whole set up costs about ~20 eur a month which include 4 [cx23](https://www.hetzner.com/cloud) servers and a Load Balancer.
The servers are configured so 1 server is the control plane while the other 3 are worker nodes. The worker node's configuration is automated via scripts, so the nodes can be conisdered to be ephemeral.

I use Terraform to manage and automate my infrastructure components.

### Automation scripts
`infra/scripts/bootsrap` provides a set of bootstrap scripts which configure each node with all the preqrequistes to run workloads on the cluster, in a fully automated way. The scripts handle automated vpn set up, containerd installation, kubernetes installation, longhorn installation for each worker node. `infra/scripts/bootstrap/reset-nodes/sh` handles destroying and re-creating the worker nodes, which can be set to be a variable number. This serves as a hard reset for the workers. Because the node setup is automated, i can save costs by bringing down a worker node (or all worker nodes) and provision them when needed. When reseting the workers, the next `terraform apply` is run with the var `allow_public_ssh=true`, which sets a dynamic rule in the Hetzner cloud firewall to allow incoming ssh connections to nodes but only from my local pc's public ip.
The bootstrap script `infra/scripts/bootstrap/bootstrap_workers.sh` is the main entrypoint. It sets up the vpn, tests connectivity and runs each modular bootstrap script on each worker node.`

### Issues with ephemeral nodes

An issue which arised was longhorn disks would degrade and not be usable/schedulable after each reset of the nodes. After much digging, the issue was due to mismatching Disk UUID's. In essence, when a worker node would get recreated, i was assigning the same name to the worker. That name is referenced by longhorn internally and is bound to the Disk UUID. But when a new node gets created, Hetzner provisions a new disk with a new UUID. So while this is a new node, having the old name meant that longhorn was still referencing the old disk, but a new disk was provisioned, leading to this error. The fix was to make the node names unique, so every time they are recreated they have a unique name. The easiest way to do this is with a timestamp.

### Networking

The nodes communicate via a private network in Hetzner. External access is done through SSH + VPN. I have Wireguard set up as vpn on the nodes, with the worker nodes + my local pc as peers. All wireguard vpn traffic goes through the control plane which acts as a proxy. I have multiple subnets to ensure some isolation and ease of networking rules if needed. The control plane, workers, and load balancer each reside in a seperate subnet.
A Hetzner Cloud Firewall is used to protect external acess to the nodes. I have it set up very strict, so the only way of accessing nodes is through the VPN.


## Kuberenetes
### Installation and Resources
- Kuberentes is managed and installed using [kubeadm](https://kubernetes.io/docs/reference/setup-tools/kubeadm/). 
- Container runtime for kubernetes used is [containerd](https://containerd.io/). 
- Storage is managed by [Longhorn](https://longhorn.io/) which provisions volumes from my nodes local storage.
- Database currently used is Postgresql, administered with [CloudNativePG](https://cloudnative-pg.io/docs/1.28/)
- TLS certificates are managed by [cert-manager](https://cert-manager.io/). (Certificates are issued by LetsEncrypt)

### Ingress
I have 2 ingress controllers set up (using ingress-nginx): 
- `public nginx ingress controller`
- `private nginx ingress controller`

With the private ingress controller, i can expose my apps privately only to VPN traffic, for testing or development. For my production apps the public controller is used along with a Hetzner Load Balancer. Both ingress controllers have the option `--watch-ingress-without-class=false` so they only watch ingresses that use the respective class. This ensures no mix ups and each controller is isolated from the other.

#### Private Ingress
The private ingress controller is set up so that internal vpn traffic can be routed accordingly to my private applications. I use it for monitoring applications as well as remote db access.
An ingress can be created for each private application, using the specified private ingress class. Then i make mapping from the host to any node vpn ip in `/etc/hosts`. For example:
```
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: longhorn-ingress
  namespace: longhorn-system
  annotations:
    nginx.ingress.kubernetes.io/auth-type: basic
    nginx.ingress.kubernetes.io/ssl-redirect: 'false'
    nginx.ingress.kubernetes.io/auth-secret: basic-auth
    nginx.ingress.kubernetes.io/auth-realm: 'Authentication Required '
    nginx.ingress.kubernetes.io/proxy-body-size: 10000m
spec:
  ingressClassName: private-nginx
  rules:
    - host: longhorn.local
      http:
        paths:
          - pathType: Prefix
            path: "/"
            backend:
              service:
                name: longhorn-frontend
                port:
                  number: 80
```
The above ingress uses the `private-nginx` class and defines a some basic auth for the longhorn ui.
I access my longhorn dashboard at `http://longhorn.local`. In my hosts file i have `longhorn.local` mapped to a node vpn ip (it can be any worker node) so the ingress resolves it.

For private remote database access, the ingress controller is configured to route tcp traffic from a specific port to the appropriate database cluster service.
  


