package nodeset

import (
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
)

// MeshRange holds every mesh address (ADR 0016). Each site takes one subnet
// of meshSubnetBits from it.
var MeshRange = netip.MustParsePrefix("10.101.0.0/16")

const meshSubnetBits = 24

// Machine is one machine of the mesh: where it stands and its address there.
type Machine struct {
	Name    string
	Site    Site
	Address netip.Addr
}

// Mesh is the Node set's mesh section. Relay names the machine that carries
// the traffic between two NAT sites; it is empty when no pair needs one.
type Mesh struct {
	Relay    string
	Machines []Machine // sorted by name
}

// Machine finds a machine of the mesh by its name.
func (m *Mesh) Machine(name string) (Machine, bool) {
	i := slices.IndexFunc(m.Machines, func(c Machine) bool { return c.Name == name })
	if i < 0 {
		return Machine{}, false
	}
	return m.Machines[i], true
}

// Mesh is the set's mesh section, or nil when the file has none: then no
// machine takes part in a mesh.
func (s *Set) Mesh() *Mesh { return s.mesh }

// meshFile is the mesh section as the file writes it.
type meshFile struct {
	Relay    string                 `yaml:"relay"`
	Machines map[string]meshMachine `yaml:"machines"`
}

type meshMachine struct {
	Site    string `yaml:"site"`
	Address string `yaml:"address"`
}

// parseMesh checks every site's subnet and the mesh section, and returns the
// section in its checked form. A nil section gives a nil Mesh.
func parseMesh(sites map[string]Site, f *meshFile) (*Mesh, error) {
	subnets, err := parseMeshSubnets(sites)
	if f == nil {
		return nil, err
	}
	errs := []error{err}
	if len(f.Machines) == 0 {
		errs = append(errs, errors.New("mesh: machines is required: a file without a mesh leaves the section out"))
	}
	m := &Mesh{Relay: f.Relay}
	owners := map[netip.Addr]string{}
	for _, name := range slices.Sorted(maps.Keys(f.Machines)) {
		machine, err := parseMachine(name, f.Machines[name], sites, subnets)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if owner, taken := owners[machine.Address]; taken {
			errs = append(errs, fmt.Errorf("mesh machine %s: address %s is already the address of %s", name, machine.Address, owner))
			continue
		}
		owners[machine.Address] = name
		m.Machines = append(m.Machines, machine)
	}
	errs = append(errs, checkMachinesPerSite(sites, f), checkRelay(sites, f))
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return m, nil
}

// parseMeshSubnets reads each site's meshSubnet. A site without one is valid
// until a machine of the mesh stands on it.
func parseMeshSubnets(sites map[string]Site) (map[string]netip.Prefix, error) {
	subnets := map[string]netip.Prefix{}
	owners := map[netip.Prefix]string{}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(sites)) {
		v := sites[name].MeshSubnet
		if v == "" {
			continue
		}
		p, err := netip.ParsePrefix(v)
		if err != nil || p != p.Masked() || p.Bits() != meshSubnetBits || !MeshRange.Contains(p.Addr()) {
			errs = append(errs, fmt.Errorf("site %s: meshSubnet %q must be a /%d network inside %s", name, v, meshSubnetBits, MeshRange))
			continue
		}
		if owner, taken := owners[p]; taken {
			errs = append(errs, fmt.Errorf("site %s: meshSubnet %s is already the subnet of site %s", name, p, owner))
			continue
		}
		owners[p] = name
		subnets[name] = p
	}
	return subnets, errors.Join(errs...)
}

func parseMachine(name string, f meshMachine, sites map[string]Site, subnets map[string]netip.Prefix) (Machine, error) {
	if !dnsLabel.MatchString(name) {
		return Machine{}, fmt.Errorf("mesh machine %q: the name is not a DNS label (lower-case letters, digits and -, at most 63)", name)
	}
	site, known := sites[f.Site]
	if !known {
		return Machine{}, fmt.Errorf("mesh machine %s: site %q is not in the Node set", name, f.Site)
	}
	subnet, ok := subnets[f.Site]
	if !ok {
		return Machine{}, fmt.Errorf("mesh machine %s: site %s has no valid meshSubnet", name, f.Site)
	}
	addr, err := netip.ParseAddr(f.Address)
	if err != nil || !subnet.Contains(addr) {
		return Machine{}, fmt.Errorf("mesh machine %s: address %q must be inside %s, the subnet of site %s", name, f.Address, subnet, f.Site)
	}
	if host := addr.As4()[3]; host == 0 || host == 255 {
		return Machine{}, fmt.Errorf("mesh machine %s: address %s is the network or broadcast address of %s", name, addr, subnet)
	}
	return Machine{Name: name, Site: site, Address: addr}, nil
}

// checkMachinesPerSite refuses a second machine on a site that is not hcloud.
// Two machines of such a site talk over their LAN, through host routes that
// kluster does not build yet. The Hetzner machines share a private network.
func checkMachinesPerSite(sites map[string]Site, f *meshFile) error {
	bySite := map[string][]string{}
	for _, name := range slices.Sorted(maps.Keys(f.Machines)) {
		bySite[f.Machines[name].Site] = append(bySite[f.Machines[name].Site], name)
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(bySite)) {
		site, known := sites[name]
		if known && site.Provider != ProviderHcloud && len(bySite[name]) > 1 {
			errs = append(errs, fmt.Errorf("site %s: %d mesh machines %v, but a site that is not %s can hold only one: kluster does not build the LAN routes between two machines of one site yet",
				name, len(bySite[name]), bySite[name], ProviderHcloud))
		}
	}
	return errors.Join(errs...)
}

// checkRelay requires a relay when machines stand on two or more NAT sites,
// because such a pair has no direct path, and refuses a relay no machine can
// reach.
func checkRelay(sites map[string]Site, f *meshFile) error {
	if f.Relay == "" {
		natSites := map[string]bool{}
		for _, machine := range f.Machines {
			if sites[machine.Site].Reach == "nat" {
				natSites[machine.Site] = true
			}
		}
		if len(natSites) >= 2 {
			return fmt.Errorf("mesh: relay is required: machines stand on the NAT sites %v, and two NAT sites reach each other only through a relay",
				slices.Sorted(maps.Keys(natSites)))
		}
		return nil
	}
	relay, ok := f.Machines[f.Relay]
	if !ok {
		return fmt.Errorf("mesh: relay %q is not a machine of the mesh", f.Relay)
	}
	if site, known := sites[relay.Site]; known && site.Reach != "public" {
		return fmt.Errorf("mesh: relay %s stands on site %s, whose reach is %s: a relay needs a public address", f.Relay, relay.Site, site.Reach)
	}
	return nil
}
