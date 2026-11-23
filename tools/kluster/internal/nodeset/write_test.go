package nodeset

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

// sitesWithSubnets is a file with a comment on every part, and no machines.
const sitesWithSubnets = `# The live Node set.
sites:
  hel1:
    provider: hcloud
    reliability: high
    reach: public
    meshSubnet: 10.101.0.0/24 # the Hetzner servers
  ts1:
    provider: self-hosted
    reliability: best-effort
    reach: nat
    meshSubnet: 10.101.1.0/24
`

var tswk1 = NewMachine{Name: "tswk1", Site: "ts1", Provisioner: ProvisionerByo, MeshAddress: netip.MustParseAddr("10.101.1.1")}

func parse(t *testing.T, src string) *Set {
	t.Helper()
	s, err := Parse("live.yaml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// withoutLines is got with the lines of added removed once each, so what is
// left must be the file as it was.
func withoutLines(got string, added ...string) string {
	lines := strings.Split(got, "\n")
	for _, a := range added {
		if i := slices.Index(lines, a); i >= 0 {
			lines = slices.Delete(lines, i, i+1)
		}
	}
	return strings.Join(lines, "\n")
}

func TestWithMachineStartsTheSectionAtTheEnd(t *testing.T) {
	out, err := parse(t, sitesWithSubnets).WithMachine(tswk1)
	if err != nil {
		t.Fatal(err)
	}
	entry := "  tswk1: {site: ts1, provisioner: byo, meshAddress: 10.101.1.1}"
	if !strings.HasSuffix(string(out), "machines:\n"+entry+"\n") {
		t.Errorf("the section is not at the end:\n%s", out)
	}
	added := append(strings.Split(machinesComment, "\n"), "machines:", entry)
	if rest := withoutLines(string(out), added...); rest != sitesWithSubnets {
		t.Errorf("a line other than the new section changed:\n%s", rest)
	}
	m, ok := parse(t, string(out)).Mesh().Machine("tswk1")
	if !ok || m.Provisioner != ProvisionerByo || m.Site.Name != "ts1" || m.Address != tswk1.MeshAddress {
		t.Errorf("read back %+v, %v", m, ok)
	}
}

// The new section goes above the mesh section and its comment, so the
// comment stays with the key it describes.
func TestWithMachineStartsTheSectionAboveTheMesh(t *testing.T) {
	src := sitesWithSubnets + "# The relay.\n# Two lines.\nmesh:\n  relay: \"\"\n"
	out, err := parse(t, src).WithMachine(tswk1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "meshAddress: 10.101.1.1}\n# The relay.\n# Two lines.\nmesh:\n") {
		t.Errorf("the section is not right above the mesh comment:\n%s", out)
	}
}

func TestWithMachineAddsAfterTheLastEntry(t *testing.T) {
	src := sitesWithSubnets + `machines:
  # the Control Plane
  k8s-cp:
    site: hel1
    provisioner: hcloud
    meshAddress: 10.101.0.1
# after the section
`
	out, err := parse(t, src).WithMachine(tswk1)
	if err != nil {
		t.Fatal(err)
	}
	entry := "  tswk1: {site: ts1, provisioner: byo, meshAddress: 10.101.1.1}"
	if !strings.Contains(string(out), "meshAddress: 10.101.0.1\n"+entry+"\n# after the section\n") {
		t.Errorf("the entry is not after the last one:\n%s", out)
	}
	if rest := withoutLines(string(out), entry); rest != src {
		t.Errorf("a line other than the entry changed:\n%s", rest)
	}
}

func TestWithMachineRefuses(t *testing.T) {
	machines := sitesWithSubnets + "machines:\n  tswk1: {site: ts1, provisioner: byo, meshAddress: 10.101.1.1}\n"
	for name, tc := range map[string]struct {
		src  string
		m    NewMachine
		want string
	}{
		"declared":     {machines, tswk1, "already declares machine tswk1"},
		"flow section": {sitesWithSubnets + "machines: {}\n", tswk1, "not a block mapping with entries; add machine tswk1 by hand"},
		"invalid":      {sitesWithSubnets, NewMachine{Name: "tswk1", Site: "hel1", Provisioner: ProvisionerByo, MeshAddress: netip.MustParseAddr("10.101.0.9")}, "site hel1 holds only the Hetzner servers"},
		"second on site": {machines, NewMachine{Name: "tswk2", Site: "ts1", Provisioner: ProvisionerByo, MeshAddress: netip.MustParseAddr("10.101.1.2")},
			"can hold only one"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parse(t, tc.src).WithMachine(tc.m); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal naming %q, got %v", tc.want, err)
			}
		})
	}
}

func TestNextMeshAddressTakesTheLowestFree(t *testing.T) {
	src := sitesWithSubnets + `machines:
  k8s-cp: {site: hel1, provisioner: hcloud, meshAddress: 10.101.0.1}
  k8swk1: {site: hel1, provisioner: hcloud, meshAddress: 10.101.0.3}
`
	s := parse(t, src)
	for site, want := range map[string]string{"hel1": "10.101.0.2", "ts1": "10.101.1.1"} {
		got, err := s.NextMeshAddress(site)
		if err != nil || got != netip.MustParseAddr(want) {
			t.Errorf("NextMeshAddress(%s) = %v, %v; want %s", site, got, err, want)
		}
	}
}

func TestNextMeshAddressNeedsASubnet(t *testing.T) {
	if _, err := parse(t, twoSites).NextMeshAddress("ts1"); err == nil || !strings.Contains(err.Error(), "site ts1 has no meshSubnet") {
		t.Fatalf("got %v", err)
	}
}
