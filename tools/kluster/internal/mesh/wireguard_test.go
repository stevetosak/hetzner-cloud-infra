//go:build wireguard

// The real WireGuard test: rendered configs go through wg-quick in network
// namespaces, and packets cross them. It needs no root, because it runs inside
// a user namespace, and it needs wg, wg-quick, nft, iptables and ping:
//
//	go test -tags wireguard -run TestWireGuard -v ./internal/mesh/
package mesh_test

import (
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/mesh"
)

const (
	// inLabEnv marks the test binary that already runs in the user namespace.
	inLabEnv = "KLUSTER_MESH_LAB"
	// linkMTU is the smallest link MTU the join check accepts.
	linkMTU = 1460
	// fullPayload fills a wg-mesh packet: the MTU less the IP and ICMP headers.
	fullPayload = mesh.MTU - 28
	// tunnelWait is how long a handshake may take to complete.
	tunnelWait = 15 * time.Second
)

// TestMain starts the test binary again inside new user, network and mount
// namespaces. Everything the tests build disappears when that process exits.
func TestMain(m *testing.M) {
	if os.Getenv(inLabEnv) == "" {
		args := append([]string{"-Urnm", "--"}, os.Args...)
		cmd := exec.Command("unshare", args...)
		cmd.Env = append(os.Environ(), inLabEnv+"=1")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "wireguard test:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if out, err := exec.Command("mount", "-t", "tmpfs", "tmpfs", "/run").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "mount /run: %v: %s\n", err, out)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// TestWireGuardDirectPair joins two public machines at two sites.
func TestWireGuardDirectPair(t *testing.T) {
	l := newLab(t)
	l.publicHost("a", "198.51.100.1")
	l.publicHost("b", "198.51.100.2")
	machines := l.keyed(
		mesh.Machine{Name: "a", Site: "hel1", Public: true, Address: ip("10.101.0.1"), Endpoint: ip("198.51.100.1")},
		mesh.Machine{Name: "b", Site: "fsn1", Public: true, Address: ip("10.101.1.1"), Endpoint: ip("198.51.100.2")},
	)
	l.up("", machines)

	l.waitPing("a", "10.101.1.1")
	l.waitPing("b", "10.101.0.1")
	l.fullSizeOnly("a", "10.101.1.1")
}

// TestWireGuardNATPair joins a machine behind NAT to a public one. The public
// machine sends first, so only the keepalive of the NAT side can open the path.
func TestWireGuardNATPair(t *testing.T) {
	l := newLab(t)
	l.publicHost("hub", "198.51.100.1")
	l.natHost("home", 1, "198.51.100.11")
	l.run("hub", "ip", "address", "add", "10.0.1.5/32", "dev", "lo")
	machines := l.keyed(
		mesh.Machine{Name: "hub", Site: "hel1", Public: true, Address: ip("10.101.0.1"), Endpoint: ip("198.51.100.1"), PrivateIP: ip("10.0.1.5")},
		mesh.Machine{Name: "home", Site: "ts1", Address: ip("10.101.1.1")},
	)
	l.up("", machines)

	l.waitPing("hub", "10.101.1.1")
	l.waitPing("home", "10.101.0.1")
	l.waitPing("home", "10.0.1.5") // a Hetzner machine's private address
	l.fullSizeOnly("home", "10.101.0.1")

	if got := l.endpointHost("hub", machines[1].PublicKey); got != "198.51.100.11" {
		t.Errorf("hub sees home at %s, want the NAT router's address 198.51.100.11", got)
	}
}

// TestWireGuardRelay joins two NAT sites through the relay, on a relay whose
// FORWARD chain drops by default, as a Kubernetes node's can.
func TestWireGuardRelay(t *testing.T) {
	l := newLab(t)
	l.publicHost("relay", "198.51.100.1")
	l.natHost("east", 1, "198.51.100.11")
	l.natHost("west", 2, "198.51.100.12")
	l.run("relay", "iptables", "-P", "FORWARD", "DROP")
	l.run("relay", "sysctl", "-qw", "net.ipv4.ip_forward=1")
	machines := l.keyed(
		mesh.Machine{Name: "relay", Site: "hel1", Public: true, Address: ip("10.101.0.1"), Endpoint: ip("198.51.100.1")},
		mesh.Machine{Name: "east", Site: "ts1", Address: ip("10.101.1.1")},
		mesh.Machine{Name: "west", Site: "ts2", Address: ip("10.101.2.1")},
	)
	l.up("relay", machines)

	l.waitPing("east", "10.101.2.1")
	l.waitPing("west", "10.101.1.1")
	l.fullSizeOnly("east", "10.101.2.1")

	if peers := strings.Fields(l.run("east", "wg", "show", mesh.Interface, "peers")); len(peers) != 1 || peers[0] != machines[0].PublicKey {
		t.Errorf("east has peers %v, want only the relay", peers)
	}
}

func ip(s string) netip.Addr { return netip.MustParseAddr(s) }

// lab is one set of namespaces joined by a bridge that stands for the internet.
// The bridge drops every IP fragment.
type lab struct {
	t      *testing.T
	prefix string
	links  int
	keys   map[string]string // machine name to private key
}

var labs int

func newLab(t *testing.T) *lab {
	t.Helper()
	labs++
	l := &lab{t: t, prefix: fmt.Sprintf("lab%d-", labs), keys: map[string]string{}}
	l.namespace("wan")
	l.run("wan", "ip", "link", "add", "br0", "type", "bridge")
	l.run("wan", "ip", "link", "set", "br0", "up")
	// The kernel fragments an outer WireGuard packet that does not fit its link,
	// so without this rule an oversized tunnel packet would still arrive.
	l.run("wan", "nft", `add table bridge lab; add chain bridge lab forward { type filter hook forward priority 0 ; }; add rule bridge lab forward ip frag-off & 0x3fff != 0 drop`)
	return l
}

func (l *lab) namespace(name string) {
	l.t.Helper()
	l.must("ip", "netns", "add", l.prefix+name)
	l.run(name, "ip", "link", "set", "lo", "up")
}

// must runs a command in the lab's root namespace and returns its output.
func (l *lab) must(name string, args ...string) string {
	l.t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		l.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// run runs a command inside the namespace ns and returns its output.
func (l *lab) run(ns string, args ...string) string {
	l.t.Helper()
	return l.must("ip", append([]string{"netns", "exec", l.prefix + ns}, args...)...)
}

// attach gives ns an interface on the internet bridge, at linkMTU.
func (l *lab) attach(ns, iface, address string) {
	l.t.Helper()
	l.links++
	port := "p" + strconv.Itoa(l.links)
	l.run("wan", "ip", "link", "add", port, "mtu", strconv.Itoa(linkMTU), "type", "veth",
		"peer", "name", iface, "mtu", strconv.Itoa(linkMTU), "netns", l.prefix+ns)
	l.run("wan", "ip", "link", "set", port, "master", "br0", "up")
	l.run(ns, "ip", "address", "add", address+"/24", "dev", iface)
	l.run(ns, "ip", "link", "set", iface, "up")
}

func (l *lab) publicHost(name, address string) {
	l.t.Helper()
	l.namespace(name)
	l.attach(name, "eth0", address)
}

// natHost builds a machine on a private LAN behind its own masquerading router.
func (l *lab) natHost(name string, lan int, routerAddress string) {
	l.t.Helper()
	router := name + "-nat"
	l.namespace(router)
	l.namespace(name)
	l.attach(router, "wan0", routerAddress)
	l.run(router, "ip", "link", "add", "lan0", "mtu", strconv.Itoa(linkMTU), "type", "veth",
		"peer", "name", "eth0", "mtu", strconv.Itoa(linkMTU), "netns", l.prefix+name)
	gateway := fmt.Sprintf("192.168.%d.1", lan)
	l.run(router, "ip", "address", "add", gateway+"/24", "dev", "lan0")
	l.run(router, "ip", "link", "set", "lan0", "up")
	l.run(name, "ip", "address", "add", fmt.Sprintf("192.168.%d.2/24", lan), "dev", "eth0")
	l.run(name, "ip", "link", "set", "eth0", "up")
	l.run(name, "ip", "route", "add", "default", "via", gateway)
	l.run(router, "sysctl", "-qw", "net.ipv4.ip_forward=1")
	l.run(router, "nft", `add table ip nat; add chain ip nat post { type nat hook postrouting priority 100 ; }; add rule ip nat post oifname "wan0" masquerade`)
}

// keyed gives each machine a new key pair. The keys live only in this lab.
func (l *lab) keyed(machines ...mesh.Machine) []mesh.Machine {
	l.t.Helper()
	for i := range machines {
		private := l.must("wg", "genkey")
		cmd := exec.Command("wg", "pubkey")
		cmd.Stdin = strings.NewReader(private)
		public, err := cmd.Output()
		if err != nil {
			l.t.Fatalf("wg pubkey: %v", err)
		}
		l.keys[machines[i].Name] = private
		machines[i].PublicKey = strings.TrimSpace(string(public))
	}
	return machines
}

// up renders each machine's config and brings it up with wg-quick, as the
// wg-quick@wg-mesh unit does on a real machine.
func (l *lab) up(relay string, machines []mesh.Machine) {
	l.t.Helper()
	for _, m := range machines {
		config, err := mesh.Render(m.Name, relay, machines)
		if err != nil {
			l.t.Fatalf("render %s: %v", m.Name, err)
		}
		path := filepath.Join(l.t.TempDir(), mesh.Interface+".conf")
		if err := os.WriteFile(path, []byte(config.Text(l.keys[m.Name])), 0o600); err != nil {
			l.t.Fatal(err)
		}
		l.run(m.Name, "wg-quick", "up", path)
	}
}

// ping sends one packet that must not be fragmented.
func (l *lab) ping(from, to string, payload int) error {
	args := []string{"netns", "exec", l.prefix + from, "ping", "-n", "-q", "-c", "1", "-W", "1", "-M", "do", "-s", strconv.Itoa(payload), to}
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

// waitPing waits for the tunnel from one machine to an address to carry traffic.
func (l *lab) waitPing(from, to string) {
	l.t.Helper()
	deadline := time.Now().Add(tunnelWait)
	for {
		err := l.ping(from, to, 56)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			l.t.Fatalf("%s cannot reach %s: %v", from, to, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// fullSizeOnly proves that a packet of exactly the mesh MTU crosses a link of
// linkMTU unfragmented, and that one byte more is refused.
func (l *lab) fullSizeOnly(from, to string) {
	l.t.Helper()
	if err := l.ping(from, to, fullPayload); err != nil {
		l.t.Errorf("a full-size packet from %s to %s did not cross: %v", from, to, err)
	}
	if err := l.ping(from, to, fullPayload+1); err == nil {
		l.t.Errorf("a packet over the mesh MTU from %s to %s crossed", from, to)
	}
}

// endpointHost is the address ns last saw the peer with this public key at.
func (l *lab) endpointHost(ns, publicKey string) string {
	l.t.Helper()
	for _, line := range strings.Split(l.run(ns, "wg", "show", mesh.Interface, "endpoints"), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == publicKey {
			endpoint, err := netip.ParseAddrPort(fields[1])
			if err != nil {
				l.t.Fatalf("endpoint %q: %v", fields[1], err)
			}
			return endpoint.Addr().String()
		}
	}
	l.t.Fatalf("%s has no endpoint for the peer", ns)
	return ""
}
