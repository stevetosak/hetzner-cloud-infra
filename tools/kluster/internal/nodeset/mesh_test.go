package nodeset

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

const meshSet = `
sites:
  hel1:
    provider: hcloud
    reliability: high
    reach: public
    meshSubnet: 10.101.0.0/24
  ts1:
    provider: self-hosted
    reliability: best-effort
    reach: nat
    meshSubnet: 10.101.1.0/24
  ts2:
    provider: self-hosted
    reliability: best-effort
    reach: nat
    meshSubnet: 10.101.2.0/24
mesh:
  relay: k8swk1
  machines:
    k8swk1: {site: hel1, address: 10.101.0.11}
    k8swk2: {site: hel1, address: 10.101.0.12}
    box1: {site: ts1, address: 10.101.1.1}
    box2: {site: ts2, address: 10.101.2.1}
`

func TestParseMesh(t *testing.T) {
	s, err := Parse("live.yaml", []byte(meshSet))
	if err != nil {
		t.Fatal(err)
	}
	m := s.Mesh()
	if m == nil {
		t.Fatal("the mesh section was lost")
	}
	if m.Relay != "k8swk1" {
		t.Errorf("relay = %q", m.Relay)
	}
	var names []string
	for _, machine := range m.Machines {
		names = append(names, machine.Name)
	}
	if want := []string{"box1", "box2", "k8swk1", "k8swk2"}; !slices.Equal(names, want) {
		t.Errorf("machines = %v, want %v", names, want)
	}
	box, ok := m.Machine("box1")
	if !ok {
		t.Fatal("box1 not found")
	}
	if box.Site.Name != "ts1" || box.Site.Reach != "nat" || box.Address != netip.MustParseAddr("10.101.1.1") {
		t.Errorf("box1 = %+v", box)
	}
	if _, ok := m.Machine("k8swk9"); ok {
		t.Error("found a machine the section does not declare")
	}
}

// A file without the section is a cluster without a mesh.
func TestParseWithoutMesh(t *testing.T) {
	s, err := Parse("live.yaml", []byte(twoSites))
	if err != nil {
		t.Fatal(err)
	}
	if s.Mesh() != nil {
		t.Errorf("Mesh() = %+v, want nil", s.Mesh())
	}
}

// One NAT site reaches the public machines directly, so it needs no relay.
func TestParseMeshOneNATSiteNeedsNoRelay(t *testing.T) {
	src := strings.NewReplacer("  relay: k8swk1\n", "", "    box2: {site: ts2, address: 10.101.2.1}\n", "").Replace(meshSet)
	s, err := Parse("live.yaml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if s.Mesh().Relay != "" {
		t.Errorf("relay = %q", s.Mesh().Relay)
	}
}

// Each case changes one part of a valid file, so the refusal can only come
// from that part.
func TestParseMeshRefuses(t *testing.T) {
	for name, tc := range map[string]struct{ old, new, want string }{
		"subnet not CIDR":     {"meshSubnet: 10.101.2.0/24", "meshSubnet: ts2", `site ts2: meshSubnet "ts2" must be a /24 network`},
		"subnet outside":      {"meshSubnet: 10.101.2.0/24", "meshSubnet: 10.102.2.0/24", "inside 10.101.0.0/16"},
		"subnet size":         {"meshSubnet: 10.101.2.0/24", "meshSubnet: 10.101.2.0/25", "must be a /24 network"},
		"subnet host bits":    {"meshSubnet: 10.101.2.0/24", "meshSubnet: 10.101.2.1/24", "must be a /24 network"},
		"subnet twice":        {"meshSubnet: 10.101.2.0/24", "meshSubnet: 10.101.1.0/24", "site ts2: meshSubnet 10.101.1.0/24 is already the subnet of site ts1"},
		"unknown field":       {"address: 10.101.1.1}", "address: 10.101.1.1, port: 51821}", "field port not found"},
		"machine name":        {"box1:", "Box_1:", `mesh machine "Box_1": the name is not a DNS label`},
		"unknown site":        {"box1: {site: ts1,", "box1: {site: ts9,", `mesh machine box1: site "ts9" is not in the Node set`},
		"site without subnet": {"    meshSubnet: 10.101.2.0/24\n", "", "mesh machine box2: site ts2 has no valid meshSubnet"},
		"address not IP":      {"address: 10.101.1.1}", "address: box}", `address "box" must be inside 10.101.1.0/24`},
		"address other site":  {"address: 10.101.1.1}", "address: 10.101.2.9}", `address "10.101.2.9" must be inside 10.101.1.0/24`},
		"address IPv6":        {"address: 10.101.1.1}", `address: "::ffff:10.101.1.1"}`, "must be inside 10.101.1.0/24"},
		"network address":     {"address: 10.101.1.1}", "address: 10.101.1.0}", "network or broadcast address"},
		"broadcast address":   {"address: 10.101.1.1}", "address: 10.101.1.255}", "network or broadcast address"},
		"address twice":       {"address: 10.101.0.12}", "address: 10.101.0.11}", "address 10.101.0.11 is already the address of"},
		"second self-hosted":  {"box2: {site: ts2, address: 10.101.2.1}", "box2: {site: ts1, address: 10.101.1.2}", "kluster does not build the LAN routes"},
		"no relay":            {"  relay: k8swk1\n", "", "relay is required: machines stand on the NAT sites [ts1 ts2]"},
		"relay not a machine": {"relay: k8swk1", "relay: k8swk9", `relay "k8swk9" is not a machine of the mesh`},
		"relay behind NAT":    {"relay: k8swk1", "relay: box1", "relay box1 stands on site ts1, whose reach is nat"},
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(meshSet, tc.old) {
				t.Fatalf("the file no longer holds %q", tc.old)
			}
			_, err := Parse("live.yaml", []byte(strings.Replace(meshSet, tc.old, tc.new, 1)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal naming %q, got %v", tc.want, err)
			}
		})
	}
}

func TestParseMeshRefusesASectionWithoutMachines(t *testing.T) {
	src := twoSites + "mesh:\n  relay: k8swk1\n"
	if _, err := Parse("live.yaml", []byte(src)); err == nil || !strings.Contains(err.Error(), "machines is required") {
		t.Fatalf("got %v", err)
	}
}

// A bad subnet is a fault of the file even while no machine stands on it.
func TestParseRefusesABadSubnetWithoutAMesh(t *testing.T) {
	src := strings.Replace(twoSites, "reach: nat", "reach: nat\n    meshSubnet: 192.168.1.0/24", 1)
	if _, err := Parse("live.yaml", []byte(src)); err == nil || !strings.Contains(err.Error(), "site ts1: meshSubnet") {
		t.Fatalf("got %v", err)
	}
}
