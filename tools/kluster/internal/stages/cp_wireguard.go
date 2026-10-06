package stages

import (
	"context"
	"fmt"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// WireGuardHub makes the Control Plane the VPN hub and its router. The hub
// key is made on the host and never leaves it; only the public half is read
// back. A rebuild therefore always has a new hub key (ADR 0009).
type WireGuardHub struct {
	Address string // 10.100.0.1/24
	Port    int
	Peers   []config.Peer
}

func (WireGuardHub) Name() string    { return "wireguard-hub" }
func (WireGuardHub) Runbook() string { return "docs/runbook/control-plane.md#3-wireguard-hub" }

// hubPublicKey is where the hub's public key is kept.
const hubPublicKey = "/etc/wireguard/public.key"

func (w WireGuardHub) conf() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nAddress    = %s\nListenPort = %d\nPrivateKey = $(cat /etc/wireguard/private.key)\n", w.Address, w.Port)
	b.WriteString("\n# Worker peers are added by kluster node add; their VPN addresses are\n# declared in infra/workers/terraform.tfvars.\n")
	for _, p := range w.Peers {
		fmt.Fprintf(&b, "\n[Peer]\n# %s\nPublicKey  = %s\nAllowedIPs = %s\n", p.Name, p.PublicKey, p.AllowedIPs)
	}
	return b.String()
}

func (w WireGuardHub) script() string {
	// An unquoted heredoc, so $(cat …) puts the key in on the host. A
	// resumed run keeps the key it already made.
	return `apt-get -o DPkg::Lock::Timeout=300 install -y wireguard
mkdir -p /etc/wireguard
umask 077
[ -s /etc/wireguard/private.key ] || wg genkey > /etc/wireguard/private.key
wg pubkey < /etc/wireguard/private.key > ` + hubPublicKey + `
chmod 600 /etc/wireguard/private.key

cat > /etc/wireguard/wg0.conf <<EOF
` + w.conf() + `EOF
chmod 600 /etc/wireguard/wg0.conf

systemctl enable wg-quick@wg0
systemctl restart wg-quick@wg0
`
}

func (w WireGuardHub) checks() []check {
	c := []check{
		{"wg0-up", "systemctl is-active --quiet wg-quick@wg0 && systemctl is-enabled --quiet wg-quick@wg0"},
		{"listen-port", fmt.Sprintf(`[ "$(wg show wg0 listen-port)" = %d ]`, w.Port)},
		{"address", "ip -4 -o addr show wg0 | grep -qw " + w.Address},
		// The hub routes between its own peers (ADR 0006).
		{"forwarding", `[ "$(sysctl -n net.ipv4.ip_forward)" = 1 ] && iptables -S FORWARD | head -1 | grep -qx -- '-P FORWARD ACCEPT'`},
	}
	for _, p := range w.Peers {
		c = append(c, check{"peer-" + p.Name, fmt.Sprintf("wg show wg0 allowed-ips | grep -F %q | grep -qw %q", p.PublicKey, p.AllowedIPs)})
	}
	return c
}

func (w WireGuardHub) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	return probe(ctx, h, w.checks())
}

func (w WireGuardHub) Preview(context.Context, *stage.Host) (string, error) { return w.script(), nil }

func (w WireGuardHub) Act(ctx context.Context, h *stage.Host) error {
	_, err := runScript(ctx, h, w.Name(), w.script())
	return err
}

// HubPublicKey reads the hub's public key from the host.
func HubPublicKey(ctx context.Context, h *stage.Host) (string, error) {
	out, err := h.Exec.Run(ctx, "cat "+hubPublicKey)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
