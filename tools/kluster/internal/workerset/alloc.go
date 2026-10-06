package workerset

import (
	"fmt"
	"net/netip"
	"strings"
)

// Pool is an inclusive range of addresses.
type Pool struct{ First, Last netip.Addr }

// ParsePool reads "first-last".
func ParsePool(s string) (Pool, error) {
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return Pool{}, fmt.Errorf("address pool %q is not first-last", s)
	}
	first, err := netip.ParseAddr(strings.TrimSpace(a))
	if err != nil {
		return Pool{}, fmt.Errorf("address pool %q: %w", s, err)
	}
	last, err := netip.ParseAddr(strings.TrimSpace(b))
	if err != nil {
		return Pool{}, fmt.Errorf("address pool %q: %w", s, err)
	}
	if last.Less(first) {
		return Pool{}, fmt.Errorf("address pool %q ends before it starts", s)
	}
	return Pool{first, last}, nil
}

// Lowest returns the lowest address of the pool that taken does not hold.
func (p Pool) Lowest(taken map[netip.Addr]bool) (netip.Addr, error) {
	for a := p.First; a.IsValid() && !p.Last.Less(a); a = a.Next() {
		if !taken[a] {
			return a, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("no free address in %s-%s", p.First, p.Last)
}

// Next is the entry `node add` writes for a new Worker: the lowest free
// address of each pool. reservedVPN are the VPN addresses that are not
// Workers' — the hub, kluster's probe peer and every operator peer.
func (s *Set) Next(name, serverType string, private, vpn Pool, reservedVPN []netip.Addr) (Worker, error) {
	takenPrivate := map[netip.Addr]bool{}
	takenVPN := map[netip.Addr]bool{}
	for _, a := range reservedVPN {
		takenVPN[a] = true
	}
	for _, w := range s.workers {
		for addr, taken := range map[string]map[netip.Addr]bool{w.PrivateIP: takenPrivate, w.VpnIP: takenVPN} {
			a, err := netip.ParseAddr(addr)
			if err != nil {
				return Worker{}, fmt.Errorf("worker %s: %w", w.Name, err)
			}
			taken[a] = true
		}
	}
	p, err := private.Lowest(takenPrivate)
	if err != nil {
		return Worker{}, fmt.Errorf("private address: %w", err)
	}
	v, err := vpn.Lowest(takenVPN)
	if err != nil {
		return Worker{}, fmt.Errorf("VPN address: %w", err)
	}
	return Worker{
		Name: name, PrivateIP: p.String(), VpnIP: v.String(), ServerType: serverType,
		Labels: map[string]string{"role": "worker"},
	}, nil
}
