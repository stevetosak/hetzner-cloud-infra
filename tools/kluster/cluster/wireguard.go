package cluster

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// WorkerPeer is one worker's WireGuard identity, known once its own
// wireguard pipeline stage has run.
type WorkerPeer struct {
	Name      string // node name, used only as a config comment
	VpnIP     string // e.g. "10.100.0.2"
	PublicKey string
}

// StaticPeer is an operator-declared peer (e.g. an admin laptop) from
// kluster.yaml's wireguard.peers list.
type StaticPeer struct {
	Name       string
	PublicKey  string
	AllowedIPs string // e.g. "10.100.0.69/32"
}

// NextAvailableVpnIP returns the lowest host address in subnetCIDR that is
// not the network address, not the broadcast address, and not present in
// excluded (already-assigned IPs, with or without a /mask suffix). Mirrors
// bootstrap_workers.sh's sequential ".2, .3, .4, ..." assignment, but reads
// what's actually taken instead of recomputing every worker's IP from
// scratch on every run.
func NextAvailableVpnIP(subnetCIDR string, excluded []string) (string, error) {
	_, ipNet, err := net.ParseCIDR(subnetCIDR)
	if err != nil {
		return "", fmt.Errorf("parsing subnet %q: %w", subnetCIDR, err)
	}

	excludedSet := make(map[string]bool, len(excluded))
	for _, e := range excluded {
		excludedSet[stripMask(e)] = true
	}

	network := ipNet.IP.Mask(ipNet.Mask)
	broadcast := broadcastAddr(ipNet)

	for ip := cloneIP(network); ipNet.Contains(ip); incIP(ip) {
		if ip.Equal(network) || ip.Equal(broadcast) {
			continue
		}
		s := ip.String()
		if !excludedSet[s] {
			return s, nil
		}
	}
	return "", fmt.Errorf("no available IP in subnet %s (all addresses excluded or in use)", subnetCIDR)
}

func stripMask(ip string) string {
	if idx := strings.Index(ip, "/"); idx != -1 {
		return ip[:idx]
	}
	return ip
}

func cloneIP(ip net.IP) net.IP {
	dup := make(net.IP, len(ip))
	copy(dup, ip)
	return dup
}

func incIP(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			break
		}
	}
}

func broadcastAddr(ipNet *net.IPNet) net.IP {
	broadcast := cloneIP(ipNet.IP.Mask(ipNet.Mask))
	for i := range broadcast {
		broadcast[i] |= ^ipNet.Mask[i]
	}
	return broadcast
}

// peerBlockFormat is the ONE definition of a [Peer] block's on-disk shape,
// used by every writer (RebuildCPWgConfig, AppendPeerBlock) so
// peerBlockPattern below — the one definition every reader (RemovePeerBlock,
// ExtractPeerIPs, PeerAllowedIPsByComment) parses against — never drifts
// from what's actually written. Changing this format requires updating
// peerBlockPattern to match, and the round-trip tests in
// wireguard_test.go (write via one of these, read back via the pattern)
// will fail loudly if the two fall out of sync.
const peerBlockFormat = "\n# %s\n[Peer]\nPublicKey = %s\nAllowedIPs = %s\n"

// RebuildCPWgConfig renders the control plane's full wg0.conf from scratch,
// for the full-cluster bootstrap/reset path. Mirrors the [Interface] +
// per-worker [Peer] + static-peer blocks bootstrap_workers.sh assembles via
// heredocs.
func RebuildCPWgConfig(cpVpnIP string, cpPrivateKey string, port int, workers []WorkerPeer, staticPeers []StaticPeer) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nAddress = %s/24\nPrivateKey = %s\nListenPort = %d\n", cpVpnIP, cpPrivateKey, port)

	for _, w := range workers {
		fmt.Fprintf(&b, peerBlockFormat, w.Name, w.PublicKey, w.VpnIP+"/32")
	}
	for _, p := range staticPeers {
		fmt.Fprintf(&b, peerBlockFormat, p.Name, p.PublicKey, p.AllowedIPs)
	}

	return b.String()
}

// peerBlockPattern matches one full "# comment / [Peer] / ... / AllowedIPs = x"
// block as written by peerBlockFormat, including the blank line that
// always precedes it (so removing a match leaves no orphaned blank line
// behind), capturing the comment and the AllowedIPs
// value.
var peerBlockPattern = regexp.MustCompile(`\n# (.*)\n\[Peer\]\n(?:.+\n)*?AllowedIPs = ([^\n]+)\n`)

// AppendPeerBlock adds one [Peer] block for a newly-bootstrapped node to an
// existing wg0.conf, for the single node-add path (which hot-reloads via
// `wg syncconf` instead of rebuilding the whole file).
func AppendPeerBlock(existing, comment, publicKey, allowedIPs string) string {
	block := fmt.Sprintf(peerBlockFormat, comment, publicKey, allowedIPs)
	return strings.TrimRight(existing, "\n") + "\n" + block
}

// RemovePeerBlock strips the [Peer] block whose AllowedIPs matches
// allowedIPs (e.g. "10.100.0.4/32") from an existing wg0.conf, for the
// node-remove path.
func RemovePeerBlock(existing, allowedIPs string) string {
	matches := peerBlockPattern.FindAllStringSubmatchIndex(existing, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		m := matches[i]
		// Group 2 is AllowedIPs: indices [4:6] (group 0 = whole match at
		// [0:2], group 1 = comment at [2:4], group 2 at [4:6]).
		if existing[m[4]:m[5]] == allowedIPs {
			existing = existing[:m[0]] + existing[m[1]:]
		}
	}
	return existing
}

// ExtractPeerIPs returns the AllowedIPs value of every [Peer] block in a
// wg0.conf, used to compute which VPN addresses are already taken before
// allocating a new one.
func ExtractPeerIPs(config string) []string {
	matches := peerBlockPattern.FindAllStringSubmatch(config, -1)
	ips := make([]string, len(matches))
	for i, m := range matches {
		ips[i] = m[2]
	}
	return ips
}

// PeerAllowedIPsByComment returns the AllowedIPs value of the [Peer] block
// whose comment equals name, and whether one was found. Comments are set
// to the node's stable tfvars-key name by both RebuildCPWgConfig and
// AppendPeerBlock, so this is how node-remove finds a peer without needing
// its VPN IP recorded anywhere else.
func PeerAllowedIPsByComment(config, name string) (string, bool) {
	matches := peerBlockPattern.FindAllStringSubmatch(config, -1)
	for _, m := range matches {
		if m[1] == name {
			return m[2], true
		}
	}
	return "", false
}
