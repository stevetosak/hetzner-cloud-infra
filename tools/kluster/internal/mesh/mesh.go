// Package mesh computes each machine's wg-mesh config from the machines of
// the mesh (ADR 0016). It is pure: the Stages read the keys and write the file.
//
// A pair at one site talks over the site's own network, so it gets no peer. A
// machine behind NAT starts the tunnel to a public machine and keeps it open.
// Two NAT machines at two sites have no direct path: each sends the other's
// traffic to the relay.
package mesh

import (
	"cmp"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// The values every wg-mesh interface shares.
const (
	// Interface is the name of the mesh's network interface.
	Interface = "wg-mesh"
	// Port is the UDP port a public machine listens on.
	Port = 51821
	// MTU leaves room for VXLAN inside WireGuard on the smallest planned link.
	MTU = 1400
	// Keepalive holds a NAT mapping open, in seconds.
	Keepalive = 25
	// ForwardMatch is the iptables rule of the FORWARD chain that lets the
	// relay pass traffic from one mesh peer to another: a Kubernetes node can
	// drop what it forwards.
	ForwardMatch = "-i " + Interface + " -o " + Interface + " -j ACCEPT"
)

// Machine is one member of the mesh.
type Machine struct {
	Name      string
	Site      string
	Public    bool       // the machine has a public address
	Address   netip.Addr // the mesh address
	Endpoint  netip.Addr // the public address; required when Public
	PrivateIP netip.Addr // the Hetzner private address; zero on any other machine
	PublicKey string
}

// Peer is one [Peer] section. Endpoint is zero and Keepalive 0 when the
// config has none.
type Peer struct {
	Name       string
	PublicKey  string
	AllowedIPs []netip.Prefix
	Endpoint   netip.AddrPort
	Keepalive  int
}

// Config is one machine's wg-mesh interface. ListenPort is 0 on a machine
// behind NAT, which only dials out. Forward is set on the relay.
type Config struct {
	Address    netip.Prefix
	MTU        int
	ListenPort int
	Forward    bool
	Peers      []Peer // sorted by name
}

// Routes is every address the interface carries, in byte order.
func (c Config) Routes() []string {
	var routes []string
	for _, p := range c.Peers {
		for _, allowed := range p.AllowedIPs {
			routes = append(routes, allowed.String())
		}
	}
	slices.Sort(routes)
	return routes
}

// Render computes the config of the machine named self. Every machine needs a
// public key; relay names the machine that joins two NAT sites, and may be
// empty when no such pair exists.
func Render(self, relay string, machines []Machine) (Config, error) {
	me, err := find(self, machines)
	if err != nil {
		return Config{}, err
	}
	sorted := slices.SortedFunc(slices.Values(machines), func(a, b Machine) int { return cmp.Compare(a.Name, b.Name) })
	c := Config{Address: host(me.Address), MTU: MTU, Forward: me.Name == relay}
	if me.Public {
		c.ListenPort = Port
	}
	var viaRelay []netip.Prefix
	var errs []error
	for _, other := range sorted {
		if other.PublicKey == "" {
			errs = append(errs, fmt.Errorf("machine %s has no public key", other.Name))
		}
		switch {
		case other.Name == me.Name || other.Site == me.Site:
		case !me.Public && !other.Public:
			viaRelay = append(viaRelay, host(other.Address))
		default:
			peer, err := newPeer(me, other)
			errs = append(errs, err)
			c.Peers = append(c.Peers, peer)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	if err := routeThroughRelay(c.Peers, relay, viaRelay); err != nil {
		return Config{}, fmt.Errorf("machine %s: %w", me.Name, err)
	}
	return c, nil
}

func find(name string, machines []Machine) (Machine, error) {
	i := slices.IndexFunc(machines, func(m Machine) bool { return m.Name == name })
	if i < 0 {
		return Machine{}, fmt.Errorf("machine %s is not in the mesh", name)
	}
	return machines[i], nil
}

// newPeer is the section me writes for other. The side behind NAT starts the
// tunnel, so only it sends a keepalive; a public peer is the one with an
// endpoint to dial.
func newPeer(me, other Machine) (Peer, error) {
	p := Peer{Name: other.Name, PublicKey: other.PublicKey, AllowedIPs: []netip.Prefix{host(other.Address)}}
	if other.PrivateIP.IsValid() {
		p.AllowedIPs = append(p.AllowedIPs, host(other.PrivateIP))
		sortHosts(p.AllowedIPs)
	}
	if !other.Public {
		return p, nil
	}
	if !other.Endpoint.IsValid() {
		return Peer{}, fmt.Errorf("machine %s is public but has no endpoint address", other.Name)
	}
	p.Endpoint = netip.AddrPortFrom(other.Endpoint, Port)
	if !me.Public {
		p.Keepalive = Keepalive
	}
	return p, nil
}

// routeThroughRelay adds the addresses only the relay can reach to its peer.
func routeThroughRelay(peers []Peer, relay string, addrs []netip.Prefix) error {
	if len(addrs) == 0 {
		return nil
	}
	i := slices.IndexFunc(peers, func(p Peer) bool { return p.Name == relay })
	if i < 0 {
		return fmt.Errorf("%d NAT machines need the relay, but %q is not a public peer", len(addrs), relay)
	}
	peers[i].AllowedIPs = append(peers[i].AllowedIPs, addrs...)
	sortHosts(peers[i].AllowedIPs)
	return nil
}

func host(a netip.Addr) netip.Prefix { return netip.PrefixFrom(a, a.BitLen()) }

// sortHosts gives AllowedIPs one order, so an unchanged mesh renders the same
// file and a host's config compares equal.
func sortHosts(hosts []netip.Prefix) {
	slices.SortFunc(hosts, func(a, b netip.Prefix) int { return a.Addr().Compare(b.Addr()) })
}

// Text is the wg-quick file. privateKey is the value of the PrivateKey line;
// an empty one leaves the line out, which is the form a host's file is
// compared in, so no key ever has to leave the host.
func (c Config) Text(privateKey string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Interface]\nAddress = %s\nMTU = %d\n", c.Address, c.MTU)
	if c.ListenPort != 0 {
		fmt.Fprintf(&b, "ListenPort = %d\n", c.ListenPort)
	}
	if c.Forward {
		fmt.Fprintf(&b, "PostUp = iptables -C FORWARD %[1]s 2>/dev/null || iptables -I FORWARD 1 %[1]s\n", ForwardMatch)
	}
	if privateKey != "" {
		fmt.Fprintf(&b, "PrivateKey = %s\n", privateKey)
	}
	for _, p := range c.Peers {
		fmt.Fprintf(&b, "\n[Peer]\n# %s\nPublicKey = %s\nAllowedIPs = %s\n", p.Name, p.PublicKey, joinPrefixes(p.AllowedIPs))
		if p.Endpoint.IsValid() {
			fmt.Fprintf(&b, "Endpoint = %s\n", p.Endpoint)
		}
		if p.Keepalive != 0 {
			fmt.Fprintf(&b, "PersistentKeepalive = %d\n", p.Keepalive)
		}
	}
	return b.String()
}

func joinPrefixes(prefixes []netip.Prefix) string {
	parts := make([]string, len(prefixes))
	for i, p := range prefixes {
		parts[i] = p.String()
	}
	return strings.Join(parts, ", ")
}
