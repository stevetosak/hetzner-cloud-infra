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

// The provisioners kluster knows. A machine names its own: a site's provider
// does not say how a machine of it is made, and one site can hold machines of
// two provisioners.
const (
	// ProvisionerHcloud creates a Hetzner server with Terraform. Its machines
	// are the Control Plane and the Worker set, on the hcloud site.
	ProvisionerHcloud = "hcloud"
	// ProvisionerByo takes a machine the operator has installed: bring your
	// own.
	ProvisionerByo = "byo"
)

var provisioners = []string{ProvisionerHcloud, ProvisionerByo}

// Machine is one machine of the Node set: where it stands, what provisions
// it, and its address on the mesh. Every machine of the set is a member of
// the mesh.
type Machine struct {
	Name        string
	Site        Site
	Provisioner string
	Address     netip.Addr // the mesh address
}

// ProviderID is the providerID the kubelet of a machine outside Hetzner
// sets. The hcloud cloud controller fails its lookup of that prefix and
// leaves the Node in place (ADR 0016).
func (m Machine) ProviderID() string { return "kluster://" + m.Site.Name + "/" + m.Name }

// Mesh is every machine of the Node set and the mesh's relay. Relay names the
// machine that carries the traffic between two NAT sites; it is empty when no
// pair needs one.
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

// Mesh is the set's machines and relay, or nil when the file declares no
// machine: then no machine takes part in a mesh.
func (s *Set) Mesh() *Mesh { return s.mesh }

// machineFile is one entry of the machines section as the file writes it.
type machineFile struct {
	Site        string `yaml:"site"`
	Provisioner string `yaml:"provisioner"`
	MeshAddress string `yaml:"meshAddress"`
}

// meshFile is the mesh section as the file writes it.
type meshFile struct {
	Relay string `yaml:"relay"`
}

// parseMesh checks every site's subnet, the machines and the mesh section,
// and returns them in their checked form with the subnets by site. No machine
// gives a nil Mesh.
func parseMesh(sites map[string]Site, machines map[string]machineFile, f *meshFile) (*Mesh, map[string]netip.Prefix, error) {
	subnets, err := parseMeshSubnets(sites)
	relay := ""
	if f != nil {
		relay = f.Relay
	}
	if len(machines) == 0 {
		if relay != "" {
			err = errors.Join(err, fmt.Errorf("mesh: relay %q is not a machine of the Node set: it declares no machines", relay))
		}
		return nil, subnets, err
	}
	errs := []error{err}
	m := &Mesh{Relay: relay}
	owners := map[netip.Addr]string{}
	for _, name := range slices.Sorted(maps.Keys(machines)) {
		machine, err := parseMachine(name, machines[name], sites, subnets)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if owner, taken := owners[machine.Address]; taken {
			errs = append(errs, fmt.Errorf("machine %s: meshAddress %s is already the address of %s", name, machine.Address, owner))
			continue
		}
		owners[machine.Address] = name
		m.Machines = append(m.Machines, machine)
	}
	errs = append(errs, checkMachinesPerSite(sites, machines), checkRelay(sites, machines, relay))
	if err := errors.Join(errs...); err != nil {
		return nil, subnets, err
	}
	return m, subnets, nil
}

// parseMeshSubnets reads each site's meshSubnet. A site without one is valid
// until a machine stands on it.
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

func parseMachine(name string, f machineFile, sites map[string]Site, subnets map[string]netip.Prefix) (Machine, error) {
	if !dnsLabel.MatchString(name) {
		return Machine{}, fmt.Errorf("machine %q: the name is not a DNS label (lower-case letters, digits and -, at most 63)", name)
	}
	site, known := sites[f.Site]
	if !known {
		return Machine{}, fmt.Errorf("machine %s: site %q is not in the Node set", name, f.Site)
	}
	if err := checkProvisioner(name, f.Provisioner, site); err != nil {
		return Machine{}, err
	}
	subnet, ok := subnets[f.Site]
	if !ok {
		return Machine{}, fmt.Errorf("machine %s: site %s has no valid meshSubnet", name, f.Site)
	}
	addr, err := netip.ParseAddr(f.MeshAddress)
	if err != nil || !subnet.Contains(addr) {
		return Machine{}, fmt.Errorf("machine %s: meshAddress %q must be inside %s, the subnet of site %s", name, f.MeshAddress, subnet, f.Site)
	}
	if !usableHost(addr) {
		return Machine{}, fmt.Errorf("machine %s: meshAddress %s is the network or broadcast address of %s", name, addr, subnet)
	}
	return Machine{Name: name, Site: site, Provisioner: f.Provisioner, Address: addr}, nil
}

// usableHost is false for the network and the broadcast address of a /24.
func usableHost(a netip.Addr) bool {
	host := a.As4()[3]
	return host != 0 && host != 255
}

// checkProvisioner refuses an unknown provisioner, and one that does not fit
// the site: the hcloud provisioner creates Hetzner servers, which stand on the
// hcloud site, and that site holds only them.
func checkProvisioner(name, provisioner string, site Site) error {
	switch {
	case provisioner == "":
		return fmt.Errorf("machine %s: provisioner is required (one of %v)", name, provisioners)
	case !slices.Contains(provisioners, provisioner):
		return fmt.Errorf("machine %s: provisioner %q is not one of %v", name, provisioner, provisioners)
	case provisioner == ProvisionerHcloud && site.Provider != ProviderHcloud:
		return fmt.Errorf("machine %s: provisioner %s creates Hetzner servers, and site %s's provider is %s", name, provisioner, site.Name, site.Provider)
	case provisioner != ProvisionerHcloud && site.Provider == ProviderHcloud:
		return fmt.Errorf("machine %s: site %s holds only the Hetzner servers of provisioner %s, not %s", name, site.Name, ProvisionerHcloud, provisioner)
	}
	return nil
}

// checkMachinesPerSite refuses a second machine on a site that is not hcloud.
// Two machines of such a site talk over their LAN, through host routes that
// kluster does not build yet. The Hetzner machines share a private network.
func checkMachinesPerSite(sites map[string]Site, machines map[string]machineFile) error {
	bySite := map[string][]string{}
	for _, name := range slices.Sorted(maps.Keys(machines)) {
		bySite[machines[name].Site] = append(bySite[machines[name].Site], name)
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(bySite)) {
		site, known := sites[name]
		if known && site.Provider != ProviderHcloud && len(bySite[name]) > 1 {
			errs = append(errs, fmt.Errorf("site %s: %d machines %v, but a site that is not %s can hold only one: kluster does not build the LAN routes between two machines of one site yet",
				name, len(bySite[name]), bySite[name], ProviderHcloud))
		}
	}
	return errors.Join(errs...)
}

// checkRelay requires a relay when machines stand on two or more NAT sites,
// because such a pair has no direct path, and refuses a relay no machine can
// reach.
func checkRelay(sites map[string]Site, machines map[string]machineFile, relay string) error {
	if relay == "" {
		natSites := map[string]bool{}
		for _, machine := range machines {
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
	m, ok := machines[relay]
	if !ok {
		return fmt.Errorf("mesh: relay %q is not a machine of the Node set", relay)
	}
	if site, known := sites[m.Site]; known && site.Reach != "public" {
		return fmt.Errorf("mesh: relay %s stands on site %s, whose reach is %s: a relay needs a public address", relay, m.Site, site.Reach)
	}
	return nil
}
