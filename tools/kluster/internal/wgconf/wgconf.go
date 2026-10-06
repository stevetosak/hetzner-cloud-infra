// Package wgconf reads and edits a wg-quick config in place. After a Control
// Plane build the hub has a new key, and kluster changes only the hub peer's
// PublicKey and Endpoint in the operator's config (ADR 0009); every other
// line, comment and peer stays as the operator wrote it.
//
// Hand-written on purpose: INI libraries rewrite comments and formatting and
// cannot hold the repeated [Peer] sections a wg-quick file has.
package wgconf

import (
	"fmt"
	"net/netip"
	"strings"
)

// HubPeer is the hub as one peer section of a config sees it.
type HubPeer struct {
	PublicKey string
	Endpoint  string
}

type section struct {
	name       string // "interface" or "peer"
	start, end int    // line range, header included, end exclusive
}

func sections(lines []string) []section {
	var out []section
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			if len(out) > 0 {
				out[len(out)-1].end = i
			}
			out = append(out, section{name: strings.ToLower(strings.Trim(t, "[]")), start: i})
		}
	}
	if len(out) > 0 {
		out[len(out)-1].end = len(lines)
	}
	return out
}

// keyValue splits a `Key = Value` line. Keys are case-insensitive in wg-quick.
func keyValue(line string) (string, string, bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return "", "", false
	}
	k, v, ok := strings.Cut(t, "=")
	if !ok {
		return "", "", false
	}
	return strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v), true
}

// hubSection finds the one peer section whose AllowedIPs holds hubIP.
func hubSection(lines []string, hubIP netip.Addr) (section, error) {
	var found []section
	for _, s := range sections(lines) {
		if s.name != "peer" {
			continue
		}
		for _, l := range lines[s.start:s.end] {
			k, v, ok := keyValue(l)
			if !ok || k != "allowedips" {
				continue
			}
			if routes(v, hubIP) {
				found = append(found, s)
				break
			}
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return section{}, fmt.Errorf("no [Peer] has AllowedIPs that hold the hub %s", hubIP)
	}
	return section{}, fmt.Errorf("%d [Peer] sections route the hub %s; kluster edits only one", len(found), hubIP)
}

func routes(allowed string, ip netip.Addr) bool {
	for _, p := range strings.Split(allowed, ",") {
		if pfx, err := netip.ParsePrefix(strings.TrimSpace(p)); err == nil && pfx.Contains(ip) {
			return true
		}
	}
	return false
}

// Hub reads the hub peer from conf.
func Hub(conf string, hubIP netip.Addr) (HubPeer, error) {
	lines := strings.Split(conf, "\n")
	s, err := hubSection(lines, hubIP)
	if err != nil {
		return HubPeer{}, err
	}
	var h HubPeer
	for _, l := range lines[s.start:s.end] {
		switch k, v, _ := keyValue(l); k {
		case "publickey":
			h.PublicKey = v
		case "endpoint":
			h.Endpoint = v
		}
	}
	return h, nil
}

// SetHub returns conf with the hub peer's PublicKey and Endpoint set to h.
// An Endpoint line is added after PublicKey if the section has none.
func SetHub(conf string, hubIP netip.Addr, h HubPeer) (string, error) {
	lines := strings.Split(conf, "\n")
	s, err := hubSection(lines, hubIP)
	if err != nil {
		return "", err
	}
	keyAt, endpointAt := -1, -1
	for i := s.start; i < s.end; i++ {
		switch k, _, _ := keyValue(lines[i]); k {
		case "publickey":
			keyAt = i
		case "endpoint":
			endpointAt = i
		}
	}
	if keyAt < 0 {
		return "", fmt.Errorf("the hub [Peer] has no PublicKey line")
	}
	lines[keyAt] = "PublicKey = " + h.PublicKey
	if endpointAt >= 0 {
		lines[endpointAt] = "Endpoint = " + h.Endpoint
	} else {
		lines = append(lines[:keyAt+1], append([]string{"Endpoint = " + h.Endpoint}, lines[keyAt+1:]...)...)
	}
	return strings.Join(lines, "\n"), nil
}

// RehearsalCopy is the stand-in for the operator's config that a rehearsal
// edits instead of the real one. It holds no private key: nothing ever
// brings it up.
func RehearsalCopy(laptopAddress, subnet string) string {
	return `# kluster rehearsal stand-in for the operator's wg0.conf. Never brought up:
# the rehearsal proves the edit here, and the hub with kluster's own peer.
[Interface]
Address = ` + laptopAddress + `
PrivateKey = (not held by kluster)

[Peer]
# hub
PublicKey = (set by kluster cp init)
AllowedIPs = ` + subnet + `
PersistentKeepalive = 25
`
}
