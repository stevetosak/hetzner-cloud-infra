//go:build nftables

// The real site firewall test: the rendered ruleset is loaded with nft into a
// machine built of network namespaces, and connections cross it. It needs no
// root, because it runs inside a user namespace, and it needs ip, nft, ping
// and python3:
//
//	go test -tags nftables -run TestSiteFirewallLab -v ./internal/stages/
package stages_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stages"
)

// inLabEnv marks the test binary that already runs in the user namespace.
const inLabEnv = "KLUSTER_FIREWALL_LAB"

// TestMain starts the test binary again inside new user, network and mount
// namespaces. Everything the test builds disappears when that process exits.
func TestMain(m *testing.M) {
	if os.Getenv(inLabEnv) == "" {
		cmd := exec.Command("unshare", append([]string{"-Urnm", "--"}, os.Args...)...)
		cmd.Env = append(os.Environ(), inLabEnv+"=1")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "firewall test:", err)
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

// The listener accepts and closes every connection; the client exits 0 only
// when its connection is accepted within two seconds.
const (
	listener = `import socket, sys
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("0.0.0.0", int(sys.argv[1])))
s.listen()
while True:
    c, _ = s.accept()
    c.close()
`
	client = `import socket, sys
socket.create_connection((sys.argv[1], int(sys.argv[2])), timeout=2).close()
`
)

type firewallLab struct{ t *testing.T }

func (l firewallLab) run(ns string, args ...string) (string, error) {
	out, err := exec.Command("ip", append([]string{"netns", "exec", ns}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (l firewallLab) must(ns string, args ...string) {
	l.t.Helper()
	if out, err := l.run(ns, args...); err != nil {
		l.t.Fatalf("%s: %s: %v\n%s", ns, strings.Join(args, " "), err, out)
	}
}

// link joins two namespaces with a veth pair whose ends carry the names and
// addresses given.
func (l firewallLab) link(a, aName, aAddr, b, bName, bAddr string) {
	l.t.Helper()
	l.must(a, "ip", "link", "add", aName, "type", "veth", "peer", "name", bName, "netns", b)
	l.must(a, "ip", "address", "add", aAddr, "dev", aName)
	l.must(b, "ip", "address", "add", bAddr, "dev", bName)
	l.must(a, "ip", "link", "set", aName, "up")
	l.must(b, "ip", "link", "set", bName, "up")
}

func (l firewallLab) listen(ns string, port int) {
	l.t.Helper()
	cmd := exec.Command("ip", "netns", "exec", ns, "python3", "-I", "-c", listener, fmt.Sprint(port))
	if err := cmd.Start(); err != nil {
		l.t.Fatal(err)
	}
	l.t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for i := 0; i < 50; i++ {
		if out, _ := l.run(ns, "ss", "-ltnH", fmt.Sprintf("sport = :%d", port)); out != "" {
			return
		}
		_, _ = l.run(ns, "sleep", "0.1")
	}
	l.t.Fatalf("%s: nothing listens on %d", ns, port)
}

func (l firewallLab) connects(ns, addr string, port int) bool {
	_, err := l.run(ns, "python3", "-I", "-c", client, addr, fmt.Sprint(port))
	return err == nil
}

func (l firewallLab) pings(ns, addr string) bool {
	_, err := l.run(ns, "ping", "-n", "-q", "-c", "1", "-W", "1", addr)
	return err == nil
}

// TestSiteFirewallLab builds a self-hosted machine with a LAN, a mesh link to
// the Control Plane and one pod, then loads the rules.
func TestSiteFirewallLab(t *testing.T) {
	l := firewallLab{t: t}
	for _, ns := range []string{"byo", "lan", "cp", "pod"} {
		if out, err := exec.Command("ip", "netns", "add", ns).CombinedOutput(); err != nil {
			t.Fatalf("ip netns add %s: %v\n%s", ns, err, out)
		}
		l.must(ns, "ip", "link", "set", "lo", "up")
	}
	// The LAN's router also stands for the internet at 198.51.100.1.
	l.link("byo", "eth0", "192.168.1.10/24", "lan", "eth0", "192.168.1.1/24")
	l.must("lan", "ip", "address", "add", "198.51.100.1/32", "dev", "lo")
	l.must("lan", "ip", "route", "add", "10.244.5.0/24", "via", "192.168.1.10")
	l.must("byo", "ip", "route", "add", "default", "via", "192.168.1.1")
	// The mesh link stands for wg-mesh: only its name matters to the rules.
	l.link("byo", "wg-mesh", "10.101.1.1/16", "cp", "eth0", "10.101.0.1/16")
	l.must("cp", "ip", "route", "add", "10.244.5.0/24", "via", "10.101.1.1")
	l.link("byo", "cni0", "10.244.5.1/24", "pod", "eth0", "10.244.5.2/24")
	l.must("pod", "ip", "route", "add", "default", "via", "10.244.5.1")
	l.must("byo", "sysctl", "-qw", "net.ipv4.ip_forward=1")
	l.listen("byo", 22)
	l.listen("lan", 8080)

	// Without the rules everything passes, so each refusal below is theirs.
	if !l.connects("lan", "192.168.1.10", 22) || !l.pings("pod", "192.168.1.1") {
		t.Fatal("the lab does not pass traffic before the rules are loaded")
	}

	f := stages.SiteFirewall{}
	file := filepath.Join(t.TempDir(), "site-firewall.nft")
	if err := os.WriteFile(file, []byte(f.File()), 0o600); err != nil {
		t.Fatal(err)
	}
	// Twice: a reload must replace the table, not fail on it.
	l.must("byo", "nft", "-f", file)
	l.must("byo", "nft", "-f", file)

	for _, c := range []struct {
		what string
		got  bool
		want bool
	}{
		{"SSH from the LAN", l.connects("lan", "192.168.1.10", 22), false},
		{"SSH over the mesh", l.connects("cp", "10.101.1.1", 22), true},
		{"SSH from a pod", l.connects("pod", "10.244.5.1", 22), true},
		{"a ping from the LAN", l.pings("lan", "192.168.1.10"), true},
		{"a connection out to the LAN", l.connects("byo", "192.168.1.1", 8080), true},
		{"a pod to the LAN", l.pings("pod", "192.168.1.1"), false},
		{"a pod to a LAN service", l.connects("pod", "192.168.1.1", 8080), false},
		{"a pod to the internet", l.pings("pod", "198.51.100.1"), true},
		{"a pod to the Control Plane", l.pings("pod", "10.101.0.1"), true},
	} {
		if c.got != c.want {
			t.Errorf("%s: passed %v, want %v", c.what, c.got, c.want)
		}
	}

	// The Probe reads the loaded table's comment; nft must print it so.
	out, err := l.run("byo", "nft", "list", "table", "inet", "kluster_site")
	if err != nil {
		t.Fatal(err)
	}
	var comment string
	for _, line := range strings.Split(f.File(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), `comment "sha256:`) {
			comment = strings.TrimSpace(line)
		}
	}
	if comment == "" || !strings.Contains(out, comment) {
		t.Errorf("the loaded table does not carry the file's %q:\n%s", comment, out)
	}
}
