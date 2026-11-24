package nodeset

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// NextMeshAddress is the lowest address of the site's meshSubnet that no
// machine holds, from .1 up.
func (s *Set) NextMeshAddress(site string) (netip.Addr, error) {
	subnet, ok := s.subnets[site]
	if !ok {
		return netip.Addr{}, fmt.Errorf("site %s has no meshSubnet: give it a /%d inside %s first", site, meshSubnetBits, MeshRange)
	}
	taken := map[netip.Addr]bool{}
	if s.mesh != nil {
		for _, m := range s.mesh.Machines {
			taken[m.Address] = true
		}
	}
	for a := subnet.Addr().Next(); subnet.Contains(a); a = a.Next() {
		if usableHost(a) && !taken[a] {
			return a, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("site %s: every address of %s is taken", site, subnet)
}

// NewMachine is what run 1 of `node add` declares: the fields of one entry of
// the machines section.
type NewMachine struct {
	Name        string
	Site        string
	Provisioner string
	MeshAddress netip.Addr
}

func (m NewMachine) line(indent string) string {
	return fmt.Sprintf("%s%s: {site: %s, provisioner: %s, meshAddress: %s}", indent, m.Name, m.Site, m.Provisioner, m.MeshAddress)
}

// machinesComment heads a machines section that kluster writes into a file
// that had none.
const machinesComment = `# Every machine of the mesh: its site, what provisions it (hcloud: the
# Control Plane and the Worker set; byo: a machine the operator installed) and
# its address on wg-mesh. ` + "`kluster node add`" + ` writes a byo entry on its first run.`

// WithMachine is the file with one more machine. Only lines are added, so
// every comment and every other line stays as it is; the result is parsed
// again, so a machine the set refuses is never written.
func (s *Set) WithMachine(m NewMachine) ([]byte, error) {
	if s.mesh != nil {
		if _, ok := s.mesh.Machine(m.Name); ok {
			return nil, fmt.Errorf("%s already declares machine %s", s.Path, m.Name)
		}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(s.src, &doc); err != nil {
		return nil, fmt.Errorf("parsing the Node set %s: %w", s.Path, err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s is not a mapping", s.Path)
	}
	top := doc.Content[0]
	lines := strings.Split(strings.TrimRight(string(s.src), "\n"), "\n")
	var err error
	if _, machines := entry(top, "machines"); machines != nil {
		lines, err = intoMachines(lines, machines, m)
	} else {
		lines = newMachinesSection(lines, top, m)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w; add machine %s by hand", s.Path, err, m.Name)
	}
	out := []byte(strings.Join(lines, "\n") + "\n")
	if _, err := Parse(s.Path, out); err != nil {
		return nil, err
	}
	return out, nil
}

// WithoutMachine is the file without one machine's entry. Only the entry's
// lines go, so every other line stays as it is, a comment above the entry
// included (the diff shows it); the result is parsed again, so a set the
// parser refuses is never written. The mesh's relay is refused: the NAT sites
// would lose their path to each other.
func (s *Set) WithoutMachine(name string) ([]byte, error) {
	if s.mesh == nil {
		return nil, fmt.Errorf("%s declares no machine %s", s.Path, name)
	}
	if _, ok := s.mesh.Machine(name); !ok {
		return nil, fmt.Errorf("%s declares no machine %s", s.Path, name)
	}
	if s.mesh.Relay == name {
		return nil, fmt.Errorf("%s is the mesh's relay in %s: name another relay first", name, s.Path)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(s.src, &doc); err != nil {
		return nil, fmt.Errorf("parsing the Node set %s: %w", s.Path, err)
	}
	_, machines := entry(doc.Content[0], "machines")
	if machines == nil || machines.Kind != yaml.MappingNode || machines.Style&yaml.FlowStyle != 0 {
		return nil, fmt.Errorf("%s: the machines section is not a block mapping; remove machine %s by hand", s.Path, name)
	}
	if len(machines.Content) == 2 {
		return nil, fmt.Errorf("%s: %s is the only machine; remove the machines section by hand", s.Path, name)
	}
	lines := strings.Split(strings.TrimRight(string(s.src), "\n"), "\n")
	for i := 0; i+1 < len(machines.Content); i += 2 {
		key, value := machines.Content[i], machines.Content[i+1]
		if key.Value != name {
			continue
		}
		lines = slices.Delete(lines, key.Line-1, lastLine(value))
		break
	}
	out := []byte(strings.Join(lines, "\n") + "\n")
	back, err := Parse(s.Path, out)
	if err != nil {
		return nil, err
	}
	if _, still := back.Mesh().Machine(name); still {
		return nil, fmt.Errorf("%s: the entry of %s spans lines kluster cannot remove; remove it by hand", s.Path, name)
	}
	return out, nil
}

// intoMachines adds the entry after the last line of a block mapping.
func intoMachines(lines []string, machines *yaml.Node, m NewMachine) ([]string, error) {
	if machines.Kind != yaml.MappingNode || machines.Style&yaml.FlowStyle != 0 || len(machines.Content) == 0 {
		return nil, errors.New("the machines section is not a block mapping with entries")
	}
	indent := strings.Repeat(" ", machines.Content[0].Column-1)
	return slices.Insert(lines, lastLine(machines), m.line(indent)), nil
}

// newMachinesSection puts a new section before the mesh section, with the
// comment above it, or at the end of a file without one.
func newMachinesSection(lines []string, top *yaml.Node, m NewMachine) []string {
	section := append(strings.Split(machinesComment, "\n"), "machines:", m.line("  "))
	at := len(lines)
	if key, _ := entry(top, "mesh"); key != nil {
		at = key.Line - 1 - strings.Count(key.HeadComment, "\n") - min(1, len(key.HeadComment))
	}
	return slices.Insert(lines, at, section...)
}

// entry is the key and the value node of a top-level key; both nil when the
// mapping has none.
func entry(top *yaml.Node, key string) (k, v *yaml.Node) {
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == key {
			return top.Content[i], top.Content[i+1]
		}
	}
	return nil, nil
}

// lastLine is the last line (1-based) that n or a node inside it starts on.
func lastLine(n *yaml.Node) int {
	last := n.Line
	for _, c := range n.Content {
		last = max(last, lastLine(c))
	}
	return last
}
