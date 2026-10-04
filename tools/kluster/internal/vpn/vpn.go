// Package vpn is kluster's own WireGuard peer, run in the process on a
// userspace network stack (wireguard-go netstack). It needs no root and adds
// no interface or route to the laptop, so it can reach a rehearsal hub that
// has the same VPN addresses as the live one while the operator's wg0 stays
// up. kluster uses it to prove the operator route through a new hub
// (docs/runbook/control-plane.md, step 3).
package vpn

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"golang.org/x/crypto/curve25519"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// MTU of the tunnel interface; wg-quick's default.
const MTU = 1420

// Key is a WireGuard (Curve25519) key.
type Key [32]byte

// GenerateKey makes a new private key, as `wg genkey` does.
func GenerateKey() (Key, error) {
	var k Key
	if _, err := rand.Read(k[:]); err != nil {
		return k, err
	}
	k[0] &= 248
	k[31] = (k[31] & 127) | 64
	return k, nil
}

// ParseKey reads a base64 key, the form `wg` prints.
func ParseKey(s string) (Key, error) {
	var k Key
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != len(k) {
		return k, fmt.Errorf("not a WireGuard key: %q", s)
	}
	copy(k[:], b)
	return k, nil
}

// Public returns the public half of a private key.
func (k Key) Public() Key {
	var pub Key
	curve25519.ScalarBaseMult((*[32]byte)(&pub), (*[32]byte)(&k))
	return pub
}

// String is the base64 form `wg` uses.
func (k Key) String() string { return base64.StdEncoding.EncodeToString(k[:]) }

func (k Key) hex() string { return hex.EncodeToString(k[:]) }

// Peer is the one peer the tunnel talks to: the hub.
type Peer struct {
	PublicKey Key
	Endpoint  netip.AddrPort
	// AllowedIPs are the addresses reached through the hub.
	AllowedIPs []netip.Prefix
}

// Tunnel is an open in-process WireGuard interface.
type Tunnel struct {
	dev *device.Device
	net *netstack.Net
}

// Open brings up an interface with address local and private key priv,
// peered with hub. The first packet sent starts the handshake.
func Open(priv Key, local netip.Addr, hub Peer) (*Tunnel, error) {
	tun, tnet, err := netstack.CreateNetTUN([]netip.Addr{local}, nil, MTU)
	if err != nil {
		return nil, fmt.Errorf("creating the userspace interface: %w", err)
	}
	dev := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	var cfg strings.Builder
	fmt.Fprintf(&cfg, "private_key=%s\npublic_key=%s\nendpoint=%s\npersistent_keepalive_interval=5\n",
		priv.hex(), hub.PublicKey.hex(), hub.Endpoint)
	for _, p := range hub.AllowedIPs {
		fmt.Fprintf(&cfg, "allowed_ip=%s\n", p)
	}
	if err := dev.IpcSet(cfg.String()); err != nil {
		dev.Close()
		return nil, fmt.Errorf("configuring the userspace interface: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, err
	}
	return &Tunnel{dev: dev, net: tnet}, nil
}

// DialContext opens a connection through the tunnel.
func (t *Tunnel) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return t.net.DialContext(ctx, network, addr)
}

// Close takes the interface down.
func (t *Tunnel) Close() { t.dev.Close() }
