package stages

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/filediff"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/wgconf"
)

// wgConf is the wg-quick config on every host.
const wgConf = "/etc/wireguard/wg0.conf"

// WireGuardSpoke makes a Worker a spoke of the hub. It has no ListenPort:
// it dials out, so the Worker firewall needs no inbound rule (ADR 0006). The
// key is made on the host and never leaves it.
type WireGuardSpoke struct {
	Address     string // 10.100.0.2/24
	HubKey      string // `wg show wg0 public-key` on the Control Plane
	HubEndpoint string // the Control Plane's public address:51820
	AllowedIPs  string // wireguard.subnet: the hub routes to every peer
}

func (WireGuardSpoke) Name() string    { return "wireguard-spoke" }
func (WireGuardSpoke) Runbook() string { return "docs/runbook/workers.md#6-wireguard" }

func (w WireGuardSpoke) conf() string {
	return fmt.Sprintf("[Interface]\nAddress    = %s\nPrivateKey = $(cat /etc/wireguard/private.key)\n\n"+
		"[Peer]\n# the hub and router\nPublicKey           = %s\nAllowedIPs          = %s\nEndpoint            = %s\nPersistentKeepalive = 25\n",
		w.Address, w.HubKey, w.AllowedIPs, w.HubEndpoint)
}

func (w WireGuardSpoke) script() string {
	// As on the hub: an unquoted heredoc puts the key in on the host, and a
	// resumed run keeps the key it already made.
	return `apt-get -o DPkg::Lock::Timeout=300 install -y wireguard
mkdir -p /etc/wireguard
umask 077
[ -s /etc/wireguard/private.key ] || wg genkey > /etc/wireguard/private.key
wg pubkey < /etc/wireguard/private.key > ` + wgPublicKey + `
chmod 600 /etc/wireguard/private.key

cat > ` + wgConf + ` <<EOF
` + w.conf() + `EOF
chmod 600 ` + wgConf + `

systemctl enable wg-quick@wg0
systemctl restart wg-quick@wg0
`
}

func (w WireGuardSpoke) checks() []check {
	return []check{
		{"wg0-up", "systemctl is-active --quiet wg-quick@wg0 && systemctl is-enabled --quiet wg-quick@wg0"},
		{"address", "ip -4 -o addr show wg0 | grep -qw " + w.Address},
		{"no-listen-port", `! grep -qi '^ListenPort' ` + wgConf},
		{"hub-peer", fmt.Sprintf("wg show wg0 allowed-ips | grep -F %q | grep -qw %q", w.HubKey, w.AllowedIPs)},
		{"hub-endpoint", fmt.Sprintf("wg show wg0 endpoints | grep -F %q | grep -qF %q", w.HubKey, w.HubEndpoint)},
	}
}

func (w WireGuardSpoke) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	return probe(ctx, h, w.checks())
}

func (w WireGuardSpoke) Preview(context.Context, *stage.Host) (string, error) { return w.script(), nil }

func (w WireGuardSpoke) Act(ctx context.Context, h *stage.Host) error {
	if w.HubKey == "" || w.HubEndpoint == "" {
		return errors.New("no hub key or endpoint")
	}
	_, err := runScript(ctx, h, w.Name(), w.script())
	return err
}

// HubPeer adds one Worker as a peer of the hub. The hub's config is
// appended to, never regenerated, and wg-quick is never restarted there:
// the operator's own session runs through it (docs/runbook/workers.md, step
// 6). `wg set` changes the live interface; the file only has to survive a
// reboot. The file holds the hub's private key, so it is shown only as a
// redacted diff.
type HubPeer struct {
	Peer wgconf.Peer // # <name>, the Worker's key, <vpn ip>/32
	Now  func() time.Time
}

func (HubPeer) Name() string    { return "hub-peer" }
func (HubPeer) Runbook() string { return "docs/runbook/workers.md#6-wireguard" }

func (p HubPeer) wgSet() string {
	return fmt.Sprintf("wg set wg0 peer %s allowed-ips %s", p.Peer.PublicKey, p.Peer.AllowedIPs)
}

func (p HubPeer) checks() []check {
	return []check{
		{"in-file", fmt.Sprintf("grep -qF %q %s", p.Peer.PublicKey, wgConf)},
		{"live", fmt.Sprintf("wg show wg0 allowed-ips | grep -F %q | grep -qw %q", p.Peer.PublicKey, p.Peer.AllowedIPs)},
	}
}

func (p HubPeer) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	return probe(ctx, h, p.checks())
}

// edit returns the hub config now and after the edit. A file that already
// holds this exact peer, from a run that stopped before `wg set`, stays.
func (p HubPeer) edit(ctx context.Context, h *stage.Host) (string, string, error) {
	old, err := h.Exec.ReadFile(ctx, wgConf)
	if err != nil {
		return "", "", errors.New("reading the hub config failed") // the error would carry the file
	}
	for _, q := range wgconf.Peers(old) {
		if q.PublicKey == p.Peer.PublicKey && q.AllowedIPs == p.Peer.AllowedIPs {
			return old, old, nil
		}
	}
	updated, err := wgconf.AddPeer(old, p.Peer)
	return old, updated, err
}

func (p HubPeer) Preview(ctx context.Context, h *stage.Host) (string, error) {
	cmds := "# back up " + wgConf + ", append the [Peer], then\n" + p.wgSet() + "\n"
	if h.Exec == nil {
		return cmds, nil
	}
	old, updated, err := p.edit(ctx, h)
	if err != nil {
		return "", err
	}
	return cmds + filediff.Unified(wgConf, old, updated), nil
}

func (p HubPeer) Act(ctx context.Context, h *stage.Host) error {
	old, updated, err := p.edit(ctx, h)
	if err != nil {
		return err
	}
	if updated != old {
		bak := wgConf + ".bak-kluster-" + p.Now().UTC().Format("20060102T150405Z")
		if _, err := h.Exec.Run(ctx, "cp -p "+wgConf+" "+bak); err != nil {
			return err
		}
		if err := h.Exec.WriteFile(ctx, wgConf, updated+"\n", 0o600); err != nil {
			return err
		}
	}
	_, err = h.Exec.Run(ctx, p.wgSet())
	return err
}

// WireGuardPublicKey reads the public key wg0 runs with.
func WireGuardPublicKey(ctx context.Context, h *stage.Host) (string, error) {
	out, err := h.Exec.Run(ctx, "wg show wg0 public-key")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
