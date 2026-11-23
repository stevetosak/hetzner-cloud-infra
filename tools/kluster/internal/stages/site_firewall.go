package stages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/mesh"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// The site firewall's table, file and unit on the machine.
const (
	siteFirewallTable = "kluster_site"
	siteFirewallFile  = "/etc/kluster/site-firewall.nft"
	siteFirewallUnit  = "kluster-site-firewall.service"
	siteFirewallPath  = "/etc/systemd/system/" + siteFirewallUnit
)

// The address ranges of the rules. clusterRanges4 are the cluster's own:
// the private network, the services, the VPN, the mesh, the pods and the
// range kept for a later move of the pods to Cilium (ADR 0001). privateRanges4
// is every address a LAN can use, the link-local and the shared range
// included.
var (
	podRanges4     = []string{"10.244.0.0/16", "10.245.0.0/16"}
	clusterRanges4 = []string{"10.0.0.0/16", "10.96.0.0/16", "10.100.0.0/24", "10.101.0.0/16", "10.244.0.0/16", "10.245.0.0/16"}
	privateRanges4 = []string{"10.0.0.0/8", "100.64.0.0/10", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16"}
)

// SiteFirewall closes a machine the operator installed to everything but the
// cluster (ADR 0016, Amendment 2026-11-23). A new inbound connection comes
// only over wg-mesh, from the machine's own pods or from itself; replies,
// ICMP and DHCP pass. Its pods reach the cluster and the internet, and no
// other private address, such as the LAN the machine stands on. The rules are
// a table of their own, so kube-proxy and Flannel keep theirs, and a unit
// loads them at boot before the network comes up.
type SiteFirewall struct {
	// Public is set on a site with a public address: its machine takes the
	// mesh's handshakes on UDP 51821.
	Public bool
}

func (SiteFirewall) Name() string    { return "site-firewall" }
func (SiteFirewall) Runbook() string { return byoRunbook }

// rules is the table's body. Its hash is the table's comment, so the Probe
// can tell the rules the kernel holds from the ones in the file.
func (f SiteFirewall) rules() string {
	mesh51821 := ""
	if f.Public {
		mesh51821 = fmt.Sprintf("\t\tudp dport %d accept\n", mesh.Port)
	}
	return fmt.Sprintf(`	set pods {
		type ipv4_addr
		flags interval
		elements = { %s }
	}
	set cluster {
		type ipv4_addr
		flags interval
		elements = { %s }
	}
	set private {
		type ipv4_addr
		flags interval
		elements = { %s }
	}
	chain input {
		type filter hook input priority filter; policy drop;
		iifname { "lo", %q, "cni0", "flannel.1" } accept
		ct state established,related accept
		meta l4proto { icmp, ipv6-icmp } accept
		udp dport 68 accept
		ip6 saddr fe80::/10 udp dport 546 accept
%s	}
	chain forward {
		type filter hook forward priority filter; policy accept;
		ip saddr @pods ip daddr @cluster accept
		ip saddr @pods ip daddr @private drop
	}
`, strings.Join(podRanges4, ", "), strings.Join(clusterRanges4, ", "), strings.Join(privateRanges4, ", "), mesh.Interface, mesh51821)
}

func (f SiteFirewall) hash() string {
	sum := sha256.Sum256([]byte(f.rules()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// File is the ruleset file. Creating the table, deleting it and defining it
// again in one file is one transaction: a load replaces the rules whole, or
// changes nothing.
func (f SiteFirewall) File() string {
	return `#!/usr/sbin/nft -f
# Written by kluster (` + byoRunbook + `). A hand change is undone by the next run.
table inet ` + siteFirewallTable + `
delete table inet ` + siteFirewallTable + `
table inet ` + siteFirewallTable + ` {
	comment "` + f.hash() + `"
` + f.rules() + `}
`
}

const siteFirewallService = `[Unit]
Description=kluster site firewall
Documentation=https://github.com/stevetosak/hetzner-cloud-infra/blob/master/` + byoRunbook + `
Wants=network-pre.target
Before=network-pre.target shutdown.target
Conflicts=shutdown.target
DefaultDependencies=no

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/nft -f ` + siteFirewallFile + `
ExecReload=/usr/sbin/nft -f ` + siteFirewallFile + `

[Install]
WantedBy=sysinit.target
`

func (f SiteFirewall) script() string {
	return `apt-get -o DPkg::Lock::Timeout=300 install -y nftables
mkdir -p /etc/kluster
` + heredoc(siteFirewallFile, f.File()) + `nft -c -f ` + siteFirewallFile + `
` + heredoc(siteFirewallPath, siteFirewallService) + `systemctl daemon-reload
systemctl enable ` + siteFirewallUnit + `
systemctl restart ` + siteFirewallUnit + `
`
}

func (f SiteFirewall) checks() []check {
	sum := sha256.Sum256([]byte(f.File()))
	return []check{
		{"file", `[ "$(sha256sum < ` + siteFirewallFile + ` | cut -d' ' -f1)" = ` + hex.EncodeToString(sum[:]) + " ]"},
		{"loaded", "nft list table inet " + siteFirewallTable + ` | grep -qF 'comment "` + f.hash() + `"'`},
		{"unit", "systemctl is-enabled --quiet " + siteFirewallUnit + " && systemctl is-active --quiet " + siteFirewallUnit},
	}
}

func (f SiteFirewall) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	return probe(ctx, h, f.checks())
}

func (f SiteFirewall) Preview(context.Context, *stage.Host) (string, error) { return f.script(), nil }

func (f SiteFirewall) Act(ctx context.Context, h *stage.Host) error {
	_, err := runScript(ctx, h, f.Name(), f.script())
	return err
}
