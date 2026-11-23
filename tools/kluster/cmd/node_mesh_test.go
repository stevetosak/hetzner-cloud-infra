package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/mesh"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/meshstore"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/nodeset"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stages"
)

const meshNodeSet = `sites:
  hel1:
    provider: hcloud
    reliability: high
    reach: public
    blockVolumes: hcloud
    loadBalancer: hcloud
    meshSubnet: 10.101.0.0/24
  ts1:
    provider: self-hosted
    reliability: best-effort
    reach: nat
    meshSubnet: 10.101.1.0/24
machines:
  k8s-cp: {site: hel1, provisioner: hcloud, meshAddress: 10.101.0.1}
  k8swk1: {site: hel1, provisioner: hcloud, meshAddress: 10.101.0.11}
  k8swk2: {site: hel1, provisioner: hcloud, meshAddress: 10.101.0.12}
`

func meshKeyOf(b byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

// machineKeys are the keys the fake machines hold.
var machineKeys = map[string]string{"k8s-cp": meshKeyOf(1), "k8swk1": meshKeyOf(2), "k8swk2": meshKeyOf(3)}

func meshServer(name, private, public string) *cloud.Server {
	return &cloud.Server{Name: name, Location: "hel1", PublicIP: public, PrivateIPs: []string{private}}
}

func meshServers() []*cloud.Server {
	return []*cloud.Server{
		meshServer("k8s-cp", "10.0.1.5", "203.0.113.1"),
		meshServer("k8swk1", "10.0.2.6", "203.0.113.11"),
		meshServer("k8swk2", "10.0.2.7", "203.0.113.12"),
	}
}

func parseMeshSet(t *testing.T, src string) *nodeset.Set {
	t.Helper()
	set, err := nodeset.Parse("test.yaml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func meshInventoryOf(t *testing.T, src string, servers ...*cloud.Server) meshInventory {
	t.Helper()
	set := parseMeshSet(t, src)
	return meshInventory{mesh: set.Mesh(), hcloud: set.Hcloud(), controlPlane: "k8s-cp",
		workers: []string{"k8swk1", "k8swk2"}, servers: servers}
}

type fakeMeshStore struct {
	entries map[string]meshstore.Entry
	puts    []string
}

func (s *fakeMeshStore) Entries(context.Context) (map[string]meshstore.Entry, error) {
	copied := map[string]meshstore.Entry{}
	for k, v := range s.entries {
		copied[k] = v
	}
	return copied, nil
}

func (s *fakeMeshStore) Put(_ context.Context, e meshstore.Entry) error {
	s.puts = append(s.puts, e.Name)
	s.entries[e.Name] = e
	return nil
}

// meshLog records each step of a run as "machine:step".
type meshLog struct {
	steps  []string
	down   map[string]bool // machines a login fails on
	noKeys bool            // no machine has a key yet
}

func (l *meshLog) host(_ context.Context, name string) (*stage.Host, error) {
	if l.down[name] {
		return nil, errors.New("no route to host")
	}
	return &stage.Host{Name: name}, nil
}

func (l *meshLog) runStages(_ context.Context, s []stage.Stage, h *stage.Host) error {
	for _, st := range s {
		l.steps = append(l.steps, h.Name+":"+st.Name())
	}
	return nil
}

func (l *meshLog) publicKey(_ context.Context, h *stage.Host) (string, error) {
	if l.noKeys {
		return "", errors.New("no such file")
	}
	return machineKeys[h.Name], nil
}

func newTestMeshRun(t *testing.T, store *fakeMeshStore, log *meshLog, apply bool, out io.Writer) *meshRun {
	t.Helper()
	return &meshRun{
		inventory: meshInventoryOf(t, meshNodeSet, meshServers()...),
		store:     store,
		host:      log.host,
		runStages: log.runStages,
		publicKey: log.publicKey,
		apply:     apply,
		out:       out,
	}
}

func joinedEntry(name, address string) meshstore.Entry {
	return meshstore.Entry{Name: name, Site: "hel1", Address: netip.MustParseAddr(address), PublicKey: machineKeys[name]}
}

func TestMeshSortRefuses(t *testing.T) {
	const outside = `sites:
  hel1: {provider: hcloud, reliability: high, reach: public, meshSubnet: 10.101.0.0/24}
  ts1: {provider: self-hosted, reliability: best-effort, reach: nat, meshSubnet: 10.101.1.0/24}
  ovh1: {provider: ovhcloud, reliability: high, reach: public, meshSubnet: 10.101.2.0/24}
machines:
  box1: {site: ts1, provisioner: byo, meshAddress: 10.101.1.1}
  vps1: {site: ovh1, provisioner: byo, meshAddress: 10.101.2.1}
  k8swk1: {site: hel1, provisioner: hcloud, meshAddress: 10.101.0.11}
  k8swk9: {site: hel1, provisioner: hcloud, meshAddress: 10.101.0.19}
  k8swk2: {site: hel1, provisioner: hcloud, meshAddress: 10.101.0.12}
`
	i := meshInventoryOf(t, outside, meshServer("k8swk1", "10.0.2.6", "203.0.113.11"))
	i.servers = append(i.servers, &cloud.Server{Name: "k8swk9", Location: "hel1"})
	ready, refused := i.sort()
	var names []string
	for _, h := range ready {
		names = append(names, h.machine.Name)
	}
	if !slices.Equal(names, []string{"box1", "k8swk1"}) {
		t.Errorf("ready %v, want box1 and k8swk1", names)
	}
	if ready[0].server != nil {
		t.Error("a byo machine got a Hetzner server")
	}
	for name, want := range map[string]string{
		"vps1":   "byo machine vps1 stands on site ovh1, whose reach is public",
		"k8swk9": "neither k8s-cp nor in the Worker set",
		"k8swk2": "has no Hetzner server",
	} {
		if err := refused[name]; err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: refused with %v, want %q", name, err, want)
		}
	}
}

// A byo machine cannot take the name of a Hetzner server.
func TestMeshSortRefusesAByoMachineWithAWorkersName(t *testing.T) {
	src := strings.Replace(meshNodeSet, "  k8swk2: {site: hel1, provisioner: hcloud, meshAddress: 10.101.0.12}\n",
		"  k8swk2: {site: ts1, provisioner: byo, meshAddress: 10.101.1.2}\n", 1)
	_, refused := meshInventoryOf(t, src, meshServers()...).sort()
	if err := refused["k8swk2"]; err == nil || !strings.Contains(err.Error(), "k8s-cp or the Worker set holds that name") {
		t.Fatalf("got %v", err)
	}
}

func TestMeshSortRefusesAnotherLocation(t *testing.T) {
	s := meshServer("k8swk1", "10.0.2.6", "203.0.113.11")
	s.Location = "fsn1"
	_, refused := meshInventoryOf(t, meshNodeSet, s).sort()
	if err := refused["k8swk1"]; err == nil || !strings.Contains(err.Error(), `stands in "fsn1"`) {
		t.Errorf("refused with %v", err)
	}
}

func TestMeshMembersJoinTheThreeSources(t *testing.T) {
	ready, _ := meshInventoryOf(t, meshNodeSet, meshServers()...).sort()
	members, err := meshMembers(ready, map[string]meshstore.Entry{"k8swk1": joinedEntry("k8swk1", "10.101.0.11")})
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 {
		t.Fatalf("members %+v: a machine with no entry is no peer", members)
	}
	m := members[0]
	if m.Name != "k8swk1" || m.Site != "hel1" || !m.Public || m.Address != netip.MustParseAddr("10.101.0.11") ||
		m.Endpoint != netip.MustParseAddr("203.0.113.11") || m.PrivateIP != netip.MustParseAddr("10.0.2.6") ||
		m.PublicKey != machineKeys["k8swk1"] {
		t.Errorf("member %+v", m)
	}
}

func TestMeshMembersRefuseABadServer(t *testing.T) {
	entries := map[string]meshstore.Entry{"k8swk1": joinedEntry("k8swk1", "10.101.0.11")}
	for name, s := range map[string]*cloud.Server{
		"public address":  {Name: "k8swk1", PublicIP: "", PrivateIPs: []string{"10.0.2.6"}},
		"no private":      {Name: "k8swk1", PublicIP: "203.0.113.11"},
		"two private":     {Name: "k8swk1", PublicIP: "203.0.113.11", PrivateIPs: []string{"10.0.2.6", "10.0.2.7"}},
		"private address": {Name: "k8swk1", PublicIP: "203.0.113.11", PrivateIPs: []string{"nonsense"}},
	} {
		t.Run(name, func(t *testing.T) {
			machine := nodeset.Machine{Name: "k8swk1", Address: netip.MustParseAddr("10.101.0.11")}
			if _, err := meshMembers([]meshHost{{machine: machine, server: s}}, entries); err == nil {
				t.Error("a server with no usable address passed")
			}
		})
	}
}

// Every join comes before the first config, so a refused machine stops the run
// with nothing changed.
func TestMeshRunJoinsEveryMachineThenConfiguresEach(t *testing.T) {
	store := &fakeMeshStore{entries: map[string]meshstore.Entry{}}
	log := &meshLog{}
	if err := newTestMeshRun(t, store, log, true, io.Discard).run(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"k8s-cp:mesh-join-check", "k8s-cp:mesh-key",
		"k8swk1:mesh-join-check", "k8swk1:mesh-key",
		"k8swk2:mesh-join-check", "k8swk2:mesh-key",
		"k8s-cp:wireguard-mesh", "k8swk1:wireguard-mesh", "k8swk2:wireguard-mesh",
	}
	if !slices.Equal(log.steps, want) {
		t.Errorf("steps %v, want %v", log.steps, want)
	}
	if !slices.Equal(store.puts, []string{"k8s-cp", "k8swk1", "k8swk2"}) {
		t.Errorf("puts %v", store.puts)
	}
}

func TestMeshRunKeepsAnUnchangedKey(t *testing.T) {
	store := &fakeMeshStore{entries: map[string]meshstore.Entry{
		"k8s-cp": joinedEntry("k8s-cp", "10.101.0.1"),
		"k8swk1": joinedEntry("k8swk1", "10.101.0.11"),
		"k8swk2": joinedEntry("k8swk2", "10.101.0.12"),
	}}
	if err := newTestMeshRun(t, store, &meshLog{}, true, io.Discard).run(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if len(store.puts) != 0 {
		t.Errorf("wrote %v, but no key changed", store.puts)
	}
}

// A replaced machine has a new key, and the store must follow it.
func TestMeshRunWritesAChangedKey(t *testing.T) {
	old := joinedEntry("k8swk1", "10.101.0.11")
	old.PublicKey = meshKeyOf(9)
	store := &fakeMeshStore{entries: map[string]meshstore.Entry{"k8swk1": old}}
	if err := newTestMeshRun(t, store, &meshLog{}, true, io.Discard).run(context.Background(), "k8swk1"); err != nil {
		t.Fatal(err)
	}
	if store.entries["k8swk1"].PublicKey != machineKeys["k8swk1"] {
		t.Errorf("the store keeps the old key")
	}
}

// A named machine joins alone, but every joined machine gets its new config.
func TestMeshRunNamedJoinsOneAndConfiguresTheJoined(t *testing.T) {
	store := &fakeMeshStore{entries: map[string]meshstore.Entry{"k8s-cp": joinedEntry("k8s-cp", "10.101.0.1")}}
	log := &meshLog{}
	var out bytes.Buffer
	if err := newTestMeshRun(t, store, log, true, &out).run(context.Background(), "k8swk1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"k8swk1:mesh-join-check", "k8swk1:mesh-key", "k8s-cp:wireguard-mesh", "k8swk1:wireguard-mesh"}
	if !slices.Equal(log.steps, want) {
		t.Errorf("steps %v, want %v", log.steps, want)
	}
	if !strings.Contains(out.String(), "k8swk2 has not joined the mesh yet") {
		t.Errorf("output does not name the machine left out:\n%s", out.String())
	}
}

func TestMeshRunRefusesAStoreTheNodeSetDoesNotDeclare(t *testing.T) {
	store := &fakeMeshStore{entries: map[string]meshstore.Entry{
		"intruder": {Name: "intruder", Site: "hel1", Address: netip.MustParseAddr("10.101.0.99"), PublicKey: meshKeyOf(7)},
	}}
	log := &meshLog{}
	err := newTestMeshRun(t, store, log, true, io.Discard).run(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "intruder is not a machine of the Node set's mesh") {
		t.Fatalf("err = %v", err)
	}
	if len(log.steps) != 0 {
		t.Errorf("steps ran before the store was checked: %v", log.steps)
	}
}

func TestMeshRunRefusesAnUnknownName(t *testing.T) {
	store := &fakeMeshStore{entries: map[string]meshstore.Entry{}}
	err := newTestMeshRun(t, store, &meshLog{}, true, io.Discard).run(context.Background(), "k8swk9")
	if err == nil || !strings.Contains(err.Error(), "has no machine k8swk9") {
		t.Fatalf("err = %v", err)
	}
}

// A machine that is down must not keep the new peer from the others.
func TestMeshRunConfiguresTheOthersWhenOneIsDown(t *testing.T) {
	store := &fakeMeshStore{entries: map[string]meshstore.Entry{
		"k8s-cp": joinedEntry("k8s-cp", "10.101.0.1"),
		"k8swk2": joinedEntry("k8swk2", "10.101.0.12"),
	}}
	log := &meshLog{down: map[string]bool{"k8s-cp": true}}
	err := newTestMeshRun(t, store, log, true, io.Discard).run(context.Background(), "k8swk1")
	if err == nil || !strings.Contains(err.Error(), "no route to host") {
		t.Fatalf("err = %v", err)
	}
	if !slices.Contains(log.steps, "k8swk2:wireguard-mesh") {
		t.Errorf("k8swk2 got no config: %v", log.steps)
	}
}

func TestMeshRunStopsBeforeAnyConfigWhenAJoinFails(t *testing.T) {
	store := &fakeMeshStore{entries: map[string]meshstore.Entry{}}
	log := &meshLog{down: map[string]bool{"k8swk1": true}}
	if err := newTestMeshRun(t, store, log, true, io.Discard).run(context.Background(), ""); err == nil {
		t.Fatal("a failed join passed")
	}
	for _, s := range log.steps {
		if strings.HasSuffix(s, ":wireguard-mesh") {
			t.Errorf("a config changed after a failed join: %v", log.steps)
		}
	}
}

func TestMeshRunPlanModeWritesNothing(t *testing.T) {
	store := &fakeMeshStore{entries: map[string]meshstore.Entry{}}
	var out bytes.Buffer
	if err := newTestMeshRun(t, store, &meshLog{}, false, &out).run(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if len(store.puts) != 0 {
		t.Errorf("Plan Mode wrote %v", store.puts)
	}
	if !strings.Contains(out.String(), "--apply writes the public key of k8swk1") {
		t.Errorf("output:\n%s", out.String())
	}
}

// In Plan Mode a new machine has no key to read, and that is no failure.
func TestMeshRunPlanModeAcceptsAMachineWithoutAKey(t *testing.T) {
	store := &fakeMeshStore{entries: map[string]meshstore.Entry{}}
	log := &meshLog{noKeys: true}
	var out bytes.Buffer
	if err := newTestMeshRun(t, store, log, false, &out).run(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "k8swk1 has no mesh key yet") {
		t.Errorf("output:\n%s", out.String())
	}
	for _, s := range log.steps {
		if strings.HasSuffix(s, ":wireguard-mesh") {
			t.Errorf("a machine with no key got a config: %v", log.steps)
		}
	}
}

func TestMeshRunApplyFailsOnAMissingKey(t *testing.T) {
	store := &fakeMeshStore{entries: map[string]meshstore.Entry{}}
	err := newTestMeshRun(t, store, &meshLog{noKeys: true}, true, io.Discard).run(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "reading the mesh public key") {
		t.Fatalf("err = %v", err)
	}
}

// meshWithOutsider adds a machine kluster cannot build: a byo machine on a
// public site.
func meshWithOutsider(t *testing.T, r *meshRun) {
	t.Helper()
	src := strings.Replace(meshNodeSet, "machines:\n",
		"  ovh1: {provider: ovhcloud, reliability: high, reach: public, meshSubnet: 10.101.2.0/24}\nmachines:\n", 1) +
		"  vps1: {site: ovh1, provisioner: byo, meshAddress: 10.101.2.1}\n"
	r.inventory = meshInventoryOf(t, src, meshServers()...)
}

func TestMeshRunOverTheWholeMeshFailsOnARefusedMachine(t *testing.T) {
	store := &fakeMeshStore{entries: map[string]meshstore.Entry{}}
	log := &meshLog{}
	var out bytes.Buffer
	r := newTestMeshRun(t, store, log, true, &out)
	meshWithOutsider(t, r)
	err := r.run(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "1 machine(s) refused") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out.String(), "refused: byo machine vps1") {
		t.Errorf("output:\n%s", out.String())
	}
	if !slices.Contains(log.steps, "k8swk1:wireguard-mesh") {
		t.Errorf("the machines kluster can reach got no config: %v", log.steps)
	}
}

// The build of one Worker does not answer for a machine it cannot reach.
func TestMeshRunOfOneMachineDoesNotFailOnAnother(t *testing.T) {
	store := &fakeMeshStore{entries: map[string]meshstore.Entry{}}
	r := newTestMeshRun(t, store, &meshLog{}, true, io.Discard)
	meshWithOutsider(t, r)
	if err := r.run(context.Background(), "k8swk1"); err != nil {
		t.Fatal(err)
	}
}

func TestMeshRunNamedRefusedMachineFails(t *testing.T) {
	store := &fakeMeshStore{entries: map[string]meshstore.Entry{}}
	r := newTestMeshRun(t, store, &meshLog{}, true, io.Discard)
	meshWithOutsider(t, r)
	err := r.run(context.Background(), "vps1")
	if err == nil || !strings.Contains(err.Error(), "whose reach is public") {
		t.Fatalf("err = %v", err)
	}
}

func TestMeshDeclares(t *testing.T) {
	if meshDeclares(nil, "k8swk1") {
		t.Error("a Node set with no mesh declares a machine")
	}
	m := parseMeshSet(t, meshNodeSet).Mesh()
	if !meshDeclares(m, "k8swk1") || meshDeclares(m, "k8swk9") {
		t.Error("meshDeclares does not follow the mesh section")
	}
}

func TestMeshSessionsDialOnceAndCloseOnlyWhatTheyDialled(t *testing.T) {
	dials, closes := 0, 0
	own := &stage.Host{Name: "k8s-cp"}
	s := &meshSessions{
		open: map[string]*stage.Host{"k8s-cp": own},
		dial: func(_ context.Context, name string) (*stage.Host, func(), error) {
			dials++
			return &stage.Host{Name: name}, func() { closes++ }, nil
		},
	}
	ctx := context.Background()
	for _, name := range []string{"k8s-cp", "k8swk1", "k8swk1"} {
		if _, err := s.host(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	s.close()
	if dials != 1 || closes != 1 {
		t.Errorf("dials %d, closes %d; want one of each", dials, closes)
	}
}

func TestMeshJoinStagesCheckBeforeTheKey(t *testing.T) {
	got := fmt.Sprint(meshJoinStages(true)[0].Name(), " ", meshJoinStages(true)[1].Name())
	if got != (stages.MeshJoinCheck{}).Name()+" "+(stages.MeshKey{}).Name() {
		t.Errorf("order %s", got)
	}
	// Only a Hetzner server's private interface is exempt from the LAN check.
	for _, hcloud := range []bool{true, false} {
		if c := meshJoinStages(hcloud)[0].(stages.MeshJoinCheck); c.Hcloud != hcloud {
			t.Errorf("meshJoinStages(%v) checks with Hcloud %v", hcloud, c.Hcloud)
		}
	}
}

// A byo machine behind NAT joins like any other, with the LAN check on its
// every interface, and every joined machine gets the new peer.
func TestMeshRunJoinsAByoMachine(t *testing.T) {
	machineKeys["box1"] = meshKeyOf(9)
	t.Cleanup(func() { delete(machineKeys, "box1") })
	store := &fakeMeshStore{entries: map[string]meshstore.Entry{
		"k8s-cp": joinedEntry("k8s-cp", "10.101.0.1"),
		"k8swk1": joinedEntry("k8swk1", "10.101.0.11"),
		"k8swk2": joinedEntry("k8swk2", "10.101.0.12"),
	}}
	log := &meshLog{}
	r := newTestMeshRun(t, store, log, true, io.Discard)
	r.inventory = meshInventoryOf(t, meshNodeSet+"  box1: {site: ts1, provisioner: byo, meshAddress: 10.101.1.1}\n", meshServers()...)
	var checks []stages.MeshJoinCheck
	configs := map[string]mesh.Config{}
	r.runStages = func(ctx context.Context, s []stage.Stage, h *stage.Host) error {
		for _, st := range s {
			switch v := st.(type) {
			case stages.MeshJoinCheck:
				checks = append(checks, v)
			case stages.WireGuardMesh:
				configs[h.Name] = v.Config
			}
		}
		return log.runStages(ctx, s, h)
	}
	if err := r.run(context.Background(), "box1"); err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].Hcloud {
		t.Errorf("box1's join checks %+v, want one with its LAN checked", checks)
	}
	if !slices.Equal(store.puts, []string{"box1"}) {
		t.Errorf("store writes %v", store.puts)
	}
	box := configs["box1"]
	if len(box.Peers) != 3 || box.ListenPort != 0 {
		t.Fatalf("box1's config %+v, want 3 peers and no listen port", box)
	}
	for _, p := range box.Peers {
		if !p.Endpoint.IsValid() || p.Keepalive != mesh.Keepalive {
			t.Errorf("box1's peer %s has endpoint %v keepalive %d: a NAT machine dials out and keeps the path open", p.Name, p.Endpoint, p.Keepalive)
		}
	}
	cp := configs["k8s-cp"]
	i := slices.IndexFunc(cp.Peers, func(p mesh.Peer) bool { return p.Name == "box1" })
	if i < 0 || cp.Peers[i].Endpoint.IsValid() {
		t.Errorf("k8s-cp's peer for box1: %+v, want one without an endpoint", cp.Peers)
	}
}
