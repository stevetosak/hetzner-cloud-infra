---
title: "Build a Node VM on a self-hosted site"
description: "How a self-hosted machine running Proxmox VE gets a Node VM that kluster can join: a template from the Ubuntu 24.04 cloud image, a full clone per Node, what each setting does and why, the checks before the join, and how the operator reaches the site from outside."
type: procedure
topics: [provisioning, kubernetes]
verified: 2026-11-26
---

A self-hosted machine runs Proxmox VE on its own disk, and the cluster's Node on
it is a virtual machine. The host stays reachable while the VM is deleted and
made again, which a rehearsal followed by live needs, and the
[Elastic nodes Plan](../plans/elastic-nodes-the-cluster-grows-and-shrinks-on-spare-compute.md)
adds more VMs on the same host. The choice was made on 2026-11-24 for part B of
the [Providers Plan](../plans/providers-the-platform-adapts-to-more-than-one.md#part-b-nodes-of-any-provider-join-as-equals).
The host is Proxmox VE 9 from its ISO (ext4 with an LVM-thin pool, the
no-subscription repositories, root SSH by key only). The first VM, `ts1-wk1` on
the site `ts1`, was built as below on 2026-11-25 in the web UI of Proxmox VE
9.3; the field names are that UI's.

The VM starts from Ubuntu's cloud image, not from the installer. The image is an
installed Ubuntu Server 24.04, and cloud-init sets it up at its first boot from
a small drive that Proxmox writes: the user, the cluster key, the hostname and
the address that [Add a bring-your-own machine](./workers.md#add-a-bring-your-own-machine)
requires. A template holds one checked image and one set of settings, and each
Node is a clone of it. Devices are VirtIO wherever there is a choice: the guest
knows it is virtual and uses a simple interface that Linux supports out of the
box, instead of an emulated piece of real hardware.

## 1. Download and check the image

A dated build, `https://cloud-images.ubuntu.com/noble/<YYYYMMDD>/`, is used
rather than `current/`, which moves with every new build, so the file stops
matching a checksum read earlier. The build's checksum file is checked against
Ubuntu's key from the `ubuntu-keyring` package, on any Ubuntu machine:

```sh
d=https://cloud-images.ubuntu.com/noble/<YYYYMMDD>
curl -fsSLO "$d/SHA256SUMS" && curl -fsSLO "$d/SHA256SUMS.gpg"
gpgv --keyring /usr/share/keyrings/ubuntu-cloudimage-keyring.gpg SHA256SUMS.gpg SHA256SUMS
grep 'noble-server-cloudimg-amd64.img$' SHA256SUMS
```

`gpgv` must print `Good signature from "UEC Image Automatic Signing Key"` for
the key `D2EB44626FDDC30B513D5BB71A5D6C4C7DB87C81`. 🔴 `gpg --recv-keys` from
keyserver.ubuntu.com failed on 2026-11-25 ("Server indicated a failure"); the
keyring file needs no keyserver.

The host then downloads the image itself, so it never passes through a laptop:
**Datacenter → host → local → Import → Download from URL**.

| Field | Value | Why |
|---|---|---|
| Storage | `local`, content Import | A folder storage on the host's system disk for disk images that are copied into VMs |
| URL | the dated `.img`, then **Query URL** | Query URL reads the size and type, which shows the URL works before the download |
| File name | `noble-server-cloudimg-amd64-<YYYYMMDD>.qcow2` | The Import content takes only `.qcow2`, `.raw`, `.vmdk`, `.ova` and `.ovf`, and Ubuntu's `.img` is a qcow2 file under another name |
| Hash algorithm, Checksum | SHA-256, the value from the signed `SHA256SUMS` | Proxmox deletes a file that does not match. HTTPS proves only which server sent the file; the signed checksum proves Ubuntu built it |
| Verify certificates | on, the default | |

The task log ends with `TASK OK`.

## 2. Create the template VM

**Create VM**, with **Advanced** ticked. The template carries no Node name;
each clone gets its own.

| Tab and field | Value | Why |
|---|---|---|
| General: VM ID, Name | `9000`, `ubuntu-2404-cloud` | A high ID keeps templates apart from VMs, which start at 100 |
| OS | Do not use any media; Linux, 7.x - 2.6 Kernel | The image is an installed system and needs no installer; the guest type picks Proxmox's defaults for Linux |
| System: Machine | `q35` | A chipset with PCIe; the default `i440fx` emulates a PCI-only chipset from 1996. q35 keeps PCIe passthrough possible |
| System: BIOS | Default (SeaBIOS) | The image boots with SeaBIOS or UEFI, and SeaBIOS needs no EFI disk. The cost, no Secure Boot inside the VM, protects little when the host controls the VM anyway |
| System: SCSI Controller | VirtIO SCSI single | The fast controller, one per disk, which **IO thread** needs |
| System: Qemu Agent | on | Proxmox can shut the VM down cleanly, read its addresses and freeze its file systems for a snapshot. The image has no agent; step 5 installs it |
| System: Add TPM | off | A Node needs no virtual TPM |
| Disks | the default disk deleted | Step 3 imports the real one |
| CPU: Sockets | 1 | The host has one CPU; two sockets would show the guest a false NUMA layout |
| CPU: Cores | all of the host's threads, 12 on `ts1-wk1` | The host itself uses almost no CPU. Under full load Linux shares the time fairly between the host and the VM |
| CPU: Type | `host` | The real CPU model with all its features. The default hides features so a VM can move to a host with another CPU, which a single host never does |
| Memory | what leaves the host about 1.5 GiB free after QEMU's own share of about 0.3 GiB; 12 GiB on `ts1-wk1` | The Node's room for pods |
| Memory: Ballooning Device | off | A balloon lets the host take memory back from a running guest. The kubelet plans pods on the memory it saw at start, so memory taken back later means evicted pods or a killed VM |
| Network: Bridge, Model | `vmbr0`, VirtIO | The bridge puts the VM on the LAN as a machine of its own, with its own MAC and address; VirtIO is the fast model |
| Network: Firewall | off | kluster's site firewall inside the VM controls the Node's traffic; a second firewall is only a second place to debug |
| Confirm: Start after created | off | 🔴 The template must never boot: at the first boot cloud-init writes the machine's identity, its SSH host keys and `/etc/machine-id`, and every clone would share it |

## 3. Add the disk, the console and cloud-init

🔴 "Do not use any media" still adds an empty CD drive on IDE 2, the slot the
CloudInit drive needs. **Hardware → CD/DVD Drive (ide2) → Remove** comes first.
Then, on VM 9000:

| Where | Value | Why |
|---|---|---|
| Hardware → Add → Import Hard Disk | SCSI 0, import storage `local`, the `.qcow2`, target `local-lvm` | Copies the image into a volume of the thin pool on the system disk. A thin volume uses space only for the data it holds, and it takes snapshots |
| the same dialog: Discard, IO thread, SSD emulation | all on | Discard passes the guest's TRIM to the thin pool, so deleted data gives its space back (Ubuntu runs `fstrim` weekly). IO thread does the disk work on a host thread of its own. SSD emulation tells the guest the disk is flash, so it uses SSD I/O settings. Cache and Async IO keep their defaults, no cache and io_uring, which lose nothing at a power loss |
| Hardware → Add → Serial Port | `0` | The cloud image writes its console to `ttyS0`; the graphic console shows almost nothing |
| Hardware → Display | Serial terminal 0 | **Console** then opens a text terminal with copy and paste, where cloud-init prints its progress and the SSH host keys. The entry is in the **Graphic card** list, between "SPICE four monitors" and "VirtIO-GPU"; typing `Serial` filters the list |
| Hardware → Add → CloudInit Drive | IDE 2, `local-lvm` | A small virtual CD that Proxmox writes with the cloud-init settings, again at each start after a setting changed |
| Options → Boot Order | `scsi0` on and first, `net0` off | An imported disk is not in the boot order by itself, and the VM then finds nothing to boot. `net0` would try a network boot first |
| Cloud-Init → User | `tosak` | Replaces the image's default user `ubuntu` and keeps its sudo without a password; kluster logs in to a byo machine as `tosak` with sudo |
| Cloud-Init → Password | empty | Nobody logs in with a password, at the console or over SSH |
| Cloud-Init → DNS servers | `1.1.1.1 9.9.9.9` | Two providers, and not the router: the site firewall drops pod traffic to private addresses, and a pod with `dnsPolicy: Default` uses the Node's servers. Read in the site firewall's rules, not tested |
| Cloud-Init → DNS domain | empty | 🔴 An empty field still falls back to the host's search domain, so the VM gets it; it cannot be set to nothing. Harmless here |
| Cloud-Init → SSH public key | the cluster key, `~/.ssh/hetzner-cluster.pub` | kluster logs in with it; only the public half reaches the VM |
| Cloud-Init → Upgrade packages | Yes, the default | The first boot installs the security fixes since the image's build; a new kernel runs after the next reboot |
| right-click → Convert to template | | The VM can never start again, and its disk becomes a read-only base |

`qm config 9000` on the host proves the result: `template: 1`,
`boot: order=scsi0`, `ide2: local-lvm:vm-9000-cloudinit,media=cdrom`,
`balloon: 0`, `cpu: host`, `serial0: socket`, `vga: serial0`, and a `scsi0` line
with `discard=on,iothread=1` and `ssd=1`.

## 4. Clone a Node VM

Right-click the template → **Clone**:

| Field | Value | Why |
|---|---|---|
| VM ID | `100`, then the next free one | |
| Name | the Node name, `<site>-wk<n>` | Proxmox hands it to cloud-init as the hostname, and byo-check refuses a hostname that is not the Node name |
| Mode | Full Clone | Copies the disk, so the VM does not depend on the template. A linked clone uses less space, but the template can then be neither changed nor deleted while the clone exists |
| Target Storage | `local-lvm` | |

A clone gets a new MAC address and a new SMBIOS UUID, and at its first boot
cloud-init sees a new instance and writes new SSH host keys and a new
`/etc/machine-id`, so every Node is unique, as Kubernetes requires. Then, on the
clone:

1. **Hardware → Hard Disk (scsi0) → Disk Action → Resize**, by what makes the
   disk 100 GiB: 96.5 GiB on the 3.5 GiB image. The Node needs room for images,
   logs and `emptyDir` volumes; a Hetzner Node has 40 GB. The volume is thin,
   and cloud-init grows the partition and the file system at the first boot.
2. **Cloud-Init → IP Config (net0)**: IPv4 static, a free LAN address with its
   prefix, and the LAN's gateway; IPv6 left empty. cloud-init writes the address
   into netplan, so no reservation on the router is needed. kluster uses it only
   for the bootstrap login; after the join the site firewall closes the LAN.
   The address is checked free first: no answer to a ping or an ARP request.
3. **Options → Start at boot**: on. After a power loss the machine powers on
   (a firmware setting), Proxmox starts, and Proxmox starts the VM.
4. **Start**, then **Console**. Before the line `Cloud-init v. … finished`,
   cloud-init prints the blocks `SSH HOST KEY FINGERPRINTS` and
   `SSH HOST KEY KEYS`. Their ED25519 lines are the key for the first login and
   the fingerprint for `node add --host-key`.

## 5. Check the VM before the join

From a laptop on the LAN, the first login accepts only the key the console
showed:

```sh
echo '<address> ssh-ed25519 AAAA…' > node.known_hosts   # the console's ED25519 key line
ssh -o UserKnownHostsFile=node.known_hosts -o StrictHostKeyChecking=yes \
    -o IdentitiesOnly=yes -i ~/.ssh/hetzner-cluster tosak@<address>
```

In the VM:

```sh
cloud-init status --long          # status: done, and errors: []
hostname; . /etc/os-release; echo "$ID $VERSION_ID"; dpkg --print-architecture
test ! -e /etc/kubernetes/pki/ca.crt && echo 'no cluster CA'
sudo -n true && echo 'sudo without a password'
sudo sshd -T | grep -E '^(passwordauthentication|kbdinteractiveauthentication) '
sudo apt-get install -y qemu-guest-agent && sudo systemctl start qemu-guest-agent
```

The VM is ready when the hostname is the Node name, the release is
`ubuntu 24.04`, the architecture `amd64`, there is no cluster CA (the four
checks of the byo-check Stage, so `node add` passes them), sudo needs no
password, and both SSH password methods are `no`. On the host,
`qm agent <id> ping` exits 0 once the agent runs. A reboot from the web UI then
proves the VM comes back by itself: the same host key, and
`systemctl is-system-running` gives `running`. `ts1-wk1` passed every check on
2026-11-25. Its boot log carries `shpchp … pci_hp_register failed with error -16`,
a harmless message on q35.

🔴 The cloud-init DNS servers are not the only ones the Node uses. On
`ts1-wk1`, `/run/systemd/resolve/resolv.conf`, the file the kubelet hands to
pods (`resolvConf` in kluster's kubelet settings), also holds the router's
link-local IPv6 server and the router's search domains: systemd-networkd takes
both from the router's IPv6 advertisements, although the VM has no global IPv6
address. Every pod on the Node would inherit those search domains. Read in the
VM on 2026-11-25. kluster does not set a byo Node's resolver yet; before a byo
Node joins live, it needs public resolvers, no search domain and no DNS from
router advertisements. A rehearsal join does not wait for it.

## Reach the site from outside

The host runs Tailscale, so the operator reaches it from anywhere with no router
change and no open port: the host dials out, and Tailscale's coordination
server only exchanges the keys. The host advertises one route, the Node VM's
`/32`, so the tailnet reaches the VM through the host and nothing else on the
LAN. Port 8006 never opens to the internet.

The package comes from Tailscale's apt repository for each machine's release,
read from `/etc/os-release`, not guessed from the kernel: `trixie` on Proxmox
VE 9. The host also gets `net.ipv4.ip_forward = 1` in
`/etc/sysctl.d/99-tailscale.conf`, which the route needs. The login runs in the
operator's own shell, because its link is an access credential:

```sh
tailscale up --advertise-routes=<VM address>/32 --accept-dns=false   # the host, in the web UI's Shell
sudo tailscale up --accept-routes --accept-dns=false                 # the operator's laptop
```

In the admin console the route is approved, and key expiry is turned off for
the host, which would otherwise drop off the tailnet after 180 days.
`--accept-dns=false` leaves both machines' resolvers as they are. 🔴 With
`--accept-routes`, the laptop sends the VM's address through Tailscale even on
the LAN, so a host whose Tailscale is down also cuts the laptop off from the VM
until `tailscale set --accept-routes=false`. Proven on 2026-11-26 from a phone's
network outside the site, where the LAN did not answer: SSH to the host and to
the VM and the web UI's HTTP 200 all went through Tailscale's relay, at about
100 ms.
