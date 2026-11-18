package mesh

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

// The cluster of the tests: two Hetzner machines, one other public machine
// and two machines behind NAT at two sites.
func machines() []Machine {
	return []Machine{
		{Name: "wk1", Site: "hel1", Public: true, Address: addr("10.101.0.11"), Endpoint: addr("203.0.113.11"), PrivateIP: addr("10.0.1.11"), PublicKey: "key-wk1"},
		{Name: "wk2", Site: "hel1", Public: true, Address: addr("10.101.0.12"), Endpoint: addr("203.0.113.12"), PrivateIP: addr("10.0.1.12"), PublicKey: "key-wk2"},
		{Name: "vps1", Site: "gra1", Public: true, Address: addr("10.101.3.1"), Endpoint: addr("198.51.100.1"), PublicKey: "key-vps1"},
		{Name: "box1", Site: "ts1", Address: addr("10.101.1.1"), PublicKey: "key-box1"},
		{Name: "box2", Site: "ts2", Address: addr("10.101.2.1"), PublicKey: "key-box2"},
	}
}

func render(t *testing.T, self string) Config {
	t.Helper()
	c, err := Render(self, "wk1", machines())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func peerNames(c Config) []string {
	var names []string
	for _, p := range c.Peers {
		names = append(names, p.Name)
	}
	return names
}

func peer(t *testing.T, c Config, name string) Peer {
	t.Helper()
	i := slices.IndexFunc(c.Peers, func(p Peer) bool { return p.Name == name })
	if i < 0 {
		t.Fatalf("no peer %s in %v", name, peerNames(c))
	}
	return c.Peers[i]
}

func allowed(p Peer) string { return joinPrefixes(p.AllowedIPs) }

func TestInterface(t *testing.T) {
	public := render(t, "wk1")
	if public.Address.String() != "10.101.0.11/32" || public.MTU != 1400 || public.ListenPort != 51821 {
		t.Errorf("public machine: %+v", public)
	}
	if nat := render(t, "box1"); nat.ListenPort != 0 {
		t.Errorf("a NAT machine listens on %d", nat.ListenPort)
	}
}

// A Hetzner-only cluster has no pair at two sites, so every wg-mesh is empty.
func TestOneSiteHasNoPeers(t *testing.T) {
	c, err := Render("wk1", "", machines()[:2])
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Peers) != 0 {
		t.Errorf("peers = %v", peerNames(c))
	}
}

func TestSameSitePairHasNoPeer(t *testing.T) {
	if names := peerNames(render(t, "wk1")); slices.Contains(names, "wk2") {
		t.Errorf("wk1 has a peer at its own site: %v", names)
	}
}

func TestPublicPair(t *testing.T) {
	p := peer(t, render(t, "vps1"), "wk1")
	if got := allowed(p); got != "10.0.1.11/32, 10.101.0.11/32" {
		t.Errorf("a Hetzner peer's AllowedIPs = %s", got)
	}
	if p.Endpoint.String() != "203.0.113.11:51821" || p.Keepalive != 0 {
		t.Errorf("public to public: %+v", p)
	}
	back := peer(t, render(t, "wk1"), "vps1")
	if got := allowed(back); got != "10.101.3.1/32" {
		t.Errorf("a peer outside Hetzner has AllowedIPs %s", got)
	}
}

func TestNATToPublic(t *testing.T) {
	p := peer(t, render(t, "box1"), "vps1")
	if p.Endpoint.String() != "198.51.100.1:51821" || p.Keepalive != 25 {
		t.Errorf("NAT to public: %+v", p)
	}
}

func TestPublicToNAT(t *testing.T) {
	p := peer(t, render(t, "wk2"), "box1")
	if p.Endpoint.IsValid() || p.Keepalive != 0 {
		t.Errorf("public to NAT must wait for the tunnel: %+v", p)
	}
}

func TestNATPairGoesThroughTheRelay(t *testing.T) {
	c := render(t, "box1")
	if slices.Contains(peerNames(c), "box2") {
		t.Fatalf("box1 has a direct peer box2: %v", peerNames(c))
	}
	if got := allowed(peer(t, c, "wk1")); got != "10.0.1.11/32, 10.101.0.11/32, 10.101.2.1/32" {
		t.Errorf("the relay's AllowedIPs = %s", got)
	}
	if got := allowed(peer(t, c, "wk2")); strings.Contains(got, "10.101.2.1") {
		t.Errorf("a machine that is not the relay carries box2: %s", got)
	}
}

// The relay reaches each NAT machine directly: that is what makes it a relay.
func TestRelayHasEachNATMachineAsAPeer(t *testing.T) {
	names := peerNames(render(t, "wk1"))
	if !slices.Contains(names, "box1") || !slices.Contains(names, "box2") {
		t.Errorf("relay peers = %v", names)
	}
}

func TestPeersAreSorted(t *testing.T) {
	if names := peerNames(render(t, "box1")); !slices.IsSorted(names) {
		t.Errorf("peers = %v", names)
	}
}

func TestRenderRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		self, relay string
		change      func([]Machine)
		want        string
	}{
		"unknown self": {"wk9", "wk1", func([]Machine) {}, "machine wk9 is not in the mesh"},
		"missing key":  {"wk1", "wk1", func(m []Machine) { m[3].PublicKey = "" }, "machine box1 has no public key"},
		"no endpoint":  {"box1", "wk1", func(m []Machine) { m[2].Endpoint = netip.Addr{} }, "machine vps1 is public but has no endpoint address"},
		"no relay":     {"box1", "", func([]Machine) {}, `machine box1: 1 NAT machines need the relay, but "" is not a public peer`},
		"NAT relay":    {"box1", "box2", func([]Machine) {}, `"box2" is not a public peer`},
	} {
		t.Run(name, func(t *testing.T) {
			m := machines()
			tc.change(m)
			if _, err := Render(tc.self, tc.relay, m); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal naming %q, got %v", tc.want, err)
			}
		})
	}
}

func TestText(t *testing.T) {
	c, err := Render("box1", "wk1", []Machine{machines()[0], machines()[3], machines()[4]})
	if err != nil {
		t.Fatal(err)
	}
	want := `[Interface]
Address = 10.101.1.1/32
MTU = 1400
PrivateKey = $(cat /etc/wireguard/mesh.key)

[Peer]
# wk1
PublicKey = key-wk1
AllowedIPs = 10.0.1.11/32, 10.101.0.11/32, 10.101.2.1/32
Endpoint = 203.0.113.11:51821
PersistentKeepalive = 25
`
	if got := c.Text("$(cat /etc/wireguard/mesh.key)"); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := c.Text(""); strings.Contains(got, "PrivateKey") {
		t.Errorf("the compared form holds a key line:\n%s", got)
	}
}

// The relay must pass traffic between peers again after a reboot.
func TestTextRelayForwards(t *testing.T) {
	want := "PostUp = iptables -C FORWARD -i wg-mesh -o wg-mesh -j ACCEPT 2>/dev/null || iptables -I FORWARD 1 -i wg-mesh -o wg-mesh -j ACCEPT\n"
	relay := render(t, "wk1")
	if !relay.Forward || !strings.Contains(relay.Text(""), want) {
		t.Errorf("the relay does not forward:\n%s", relay.Text(""))
	}
	other := render(t, "wk2")
	if other.Forward || strings.Contains(other.Text(""), "PostUp") {
		t.Errorf("a machine that is not the relay forwards:\n%s", other.Text(""))
	}
}

func TestRoutes(t *testing.T) {
	got := render(t, "box1").Routes()
	want := []string{"10.0.1.11/32", "10.0.1.12/32", "10.101.0.11/32", "10.101.0.12/32", "10.101.2.1/32", "10.101.3.1/32"}
	if !slices.Equal(got, want) {
		t.Errorf("routes = %v, want %v", got, want)
	}
}

func TestTextPublicMachineListens(t *testing.T) {
	if got := render(t, "wk1").Text(""); !strings.Contains(got, "ListenPort = 51821\n") {
		t.Errorf("no ListenPort:\n%s", got)
	}
	if got := render(t, "box1").Text(""); strings.Contains(got, "ListenPort") {
		t.Errorf("a NAT machine listens:\n%s", got)
	}
}
