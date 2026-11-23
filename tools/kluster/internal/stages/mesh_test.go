package stages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/local"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/mesh"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

func meshConfig(t *testing.T, self string) mesh.Config {
	t.Helper()
	c, err := mesh.Render(self, "wk1", []mesh.Machine{
		{Name: "wk1", Site: "hel1", Public: true, Address: netip.MustParseAddr("10.101.0.11"),
			Endpoint: netip.MustParseAddr("203.0.113.11"), PrivateIP: netip.MustParseAddr("10.0.1.11"), PublicKey: "key-wk1"},
		{Name: "box1", Site: "ts1", Address: netip.MustParseAddr("10.101.1.1"), PublicKey: "key-box1"},
		{Name: "box2", Site: "ts2", Address: netip.MustParseAddr("10.101.2.1"), PublicKey: "key-box2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMeshKeyKeepsAnExistingKey(t *testing.T) {
	script := MeshKey{}.script()
	if !strings.Contains(script, "[ -s /etc/wireguard/mesh.key ] || wg genkey > /etc/wireguard/mesh.key") {
		t.Errorf("a second run would replace the key:\n%s", script)
	}
}

func TestMeshPublicKeyTrims(t *testing.T) {
	h := &stage.Host{Exec: &scriptHost{out: map[string]string{"cat /etc/wireguard/mesh.pub": "pub=\n"}}}
	got, err := MeshPublicKey(context.Background(), h)
	if err != nil || got != "pub=" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// The script must stop before it writes a config with an empty key.
func TestWireGuardMeshChecksTheKeyFirst(t *testing.T) {
	script := WireGuardMesh{Config: meshConfig(t, "box1")}.script()
	if strings.Index(script, "test -s /etc/wireguard/mesh.key") > strings.Index(script, "cat > /etc/wireguard/wg-mesh.conf") {
		t.Errorf("the key is checked after the write:\n%s", script)
	}
	if !strings.Contains(script, "PrivateKey = $(cat /etc/wireguard/mesh.key)\n") {
		t.Errorf("the key is not put in on the host:\n%s", script)
	}
}

func TestWireGuardMeshKeepsLiveTunnels(t *testing.T) {
	script := WireGuardMesh{Config: meshConfig(t, "box1")}.script()
	if !strings.Contains(script, "wg syncconf wg-mesh <(wg-quick strip wg-mesh)") {
		t.Errorf("no syncconf:\n%s", script)
	}
}

func TestWireGuardMeshForwardOnlyOnTheRelay(t *testing.T) {
	relay := WireGuardMesh{Config: meshConfig(t, "wk1")}.script()
	if !strings.Contains(relay, "iptables -I FORWARD 1 -i wg-mesh -o wg-mesh -j ACCEPT") {
		t.Errorf("the relay has no rule:\n%s", relay)
	}
	other := WireGuardMesh{Config: meshConfig(t, "box1")}.script()
	if strings.Contains(other, "iptables -I FORWARD") || !strings.Contains(other, "iptables -D FORWARD -i wg-mesh") {
		t.Errorf("another machine must lose the rule:\n%s", other)
	}
}

// The file on the host, read through confHashCmd in real bash, hashes the same
// as the form the Probe wants, whatever key it holds.
func TestConfHashIgnoresOnlyTheKey(t *testing.T) {
	c := meshConfig(t, "wk1")
	path := filepath.Join(t.TempDir(), "wg-mesh.conf")
	if err := os.WriteFile(path, []byte(c.Text("c2VjcmV0")), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := local.Exec{}.Run(context.Background(), confHashCmd(path))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(c.Text("")))
	if got := strings.TrimSpace(out); got != hex.EncodeToString(sum[:]) {
		t.Errorf("hash on the host %s differs from the wanted %x", got, sum)
	}
}

// The route check, run in real bash on the output `ip route` gives.
func TestRouteNormaliserInBash(t *testing.T) {
	stub := newStub(t, "")
	if err := os.WriteFile(filepath.Join(stub.dir, "ip"), []byte("#!/usr/bin/env bash\nprintf '10.101.2.1 scope link\\n10.0.1.11 scope link\\n10.101.0.0/24 scope link\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	w := WireGuardMesh{Config: meshConfig(t, "box1")}
	i := slices.IndexFunc(w.checks(), func(c check) bool { return c.name == "routes" })
	cmd := strings.Replace(w.checks()[i].cmd, `" = '`, `" ; echo '`, 1)
	cmd = strings.TrimSuffix(cmd, " ]")
	out, err := stub.Run(context.Background(), strings.Replace(cmd, `[ "$(`, `echo "$(`, 1))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(out, "\n")[0]; got != "10.0.1.11/32 10.101.0.0/24 10.101.2.1/32" {
		t.Errorf("normalised routes = %q", got)
	}
}

const hostAddrs = `1: lo    inet 127.0.0.1/8 scope host lo\       valid_lft forever
2: eth0    inet 192.168.1.20/24 brd 192.168.1.255 scope global eth0\       valid_lft forever
3: wg0    inet 10.100.0.5/24 scope global wg0\       valid_lft forever
4: wg-mesh    inet 10.101.1.1/32 scope global wg-mesh\       valid_lft forever
5: flannel.1    inet 10.244.3.0/32 scope global flannel.1\       valid_lft forever
6: cni0    inet 10.244.3.1/24 brd 10.244.3.255 scope global cni0\       valid_lft forever
`

func TestLANOverlapsPassesTheClusterInterfaces(t *testing.T) {
	faults, err := lanOverlaps(hostAddrs, false)
	if err != nil || len(faults) != 0 {
		t.Fatalf("faults %v, err %v", faults, err)
	}
}

func TestLANOverlapsRefuses(t *testing.T) {
	for name, tc := range map[string]struct{ lan, want string }{
		"private network": {"10.0.5.2/24", "the private network (10.0.0.0/16)"},
		"pods":            {"10.244.9.2/24", "the pods (10.244.0.0/16)"},
		"cilium pods":     {"10.245.1.2/24", "the range kept for a move of the pods to Cilium (10.245.0.0/16)"},
		"services":        {"10.96.1.2/24", "the services (10.96.0.0/16)"},
		"VPN":             {"10.100.0.9/24", "the VPN (10.100.0.0/24)"},
		"mesh":            {"10.101.7.2/24", "the mesh (10.101.0.0/16)"},
		"wider LAN":       {"10.0.0.2/8", "eth0 has 10.0.0.0/8, which overlaps the private network"},
	} {
		t.Run(name, func(t *testing.T) {
			faults, err := lanOverlaps("2: eth0    inet "+tc.lan+" scope global eth0\n", false)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.Join(faults, ";"), tc.want) {
				t.Errorf("faults = %v, want %q", faults, tc.want)
			}
		})
	}
}

func TestLANOverlapsExemptsOnlyTheHcloudPrivateNetwork(t *testing.T) {
	private := "3: enp7s0    inet 10.0.1.11/32 scope global enp7s0\n"
	if faults, _ := lanOverlaps(private, true); len(faults) != 0 {
		t.Errorf("a Hetzner machine's private interface was refused: %v", faults)
	}
	if faults, _ := lanOverlaps(private, false); len(faults) != 1 {
		t.Errorf("another machine with that address passed: %v", faults)
	}
	if faults, _ := lanOverlaps("3: enp7s0    inet 10.244.0.2/24 scope global enp7s0\n", true); len(faults) != 1 {
		t.Errorf("the exemption reached another range: %v", faults)
	}
}

func TestLANOverlapsRefusesAnUnreadableAddress(t *testing.T) {
	if _, err := lanOverlaps("2: eth0    inet nonsense scope global eth0\n", false); err == nil {
		t.Fatal("an unreadable address passed")
	}
}

func TestLinkFault(t *testing.T) {
	for name, tc := range map[string]struct{ out, fault, detail string }{
		"full path":  {"mtu=1500\nlarge=ok\nsmall=ok\n", "", "link MTU 1500, path holds 1460"},
		"exact":      {"mtu=1460\nlarge=ok\nsmall=ok\n", "", "link MTU 1460, path holds 1460"},
		"small link": {"mtu=1420\nlarge=ok\nsmall=ok\n", "the link MTU is 1420, and the mesh needs at least 1460", ""},
		"small path": {"mtu=1500\nlarge=lost\nsmall=ok\n", "the path to 1.1.1.1 drops a packet of 1460 bytes, and the mesh needs that size", ""},
		"unmeasured": {"mtu=1500\nlarge=lost\nsmall=lost\n", "", "link MTU 1500, path not measured: 1.1.1.1 does not answer"},
		"no MTU":     {"mtu=\nlarge=lost\nsmall=lost\n", "the MTU of the default route's device cannot be read", ""},
	} {
		t.Run(name, func(t *testing.T) {
			fault, detail := linkFault(tc.out)
			if fault != tc.fault || detail != tc.detail {
				t.Errorf("got %q / %q", fault, detail)
			}
		})
	}
}

func joinHost(addrs, link string) *stage.Host {
	return &stage.Host{Exec: &scriptHost{out: map[string]string{addressesRead: addrs, "dev=$(ip": link}}}
}

func TestMeshJoinCheckPasses(t *testing.T) {
	h := joinHost(hostAddrs, "mtu=1500\nlarge=ok\nsmall=ok\n")
	st, err := MeshJoinCheck{}.Probe(context.Background(), h)
	if err != nil || !st.Done || st.Detail != "no LAN overlap, link MTU 1500, path holds 1460" {
		t.Fatalf("%+v, %v", st, err)
	}
}

func TestMeshJoinCheckRefusesInActAndNamesEveryFault(t *testing.T) {
	h := joinHost("2: eth0    inet 10.244.9.2/24 scope global eth0\n", "mtu=1400\nlarge=ok\nsmall=ok\n")
	ctx := context.Background()
	st, err := MeshJoinCheck{}.Probe(ctx, h)
	if err != nil || st.Done {
		t.Fatalf("%+v, %v", st, err)
	}
	err = MeshJoinCheck{}.Act(ctx, h)
	if err == nil || !strings.Contains(err.Error(), "the pods") || !strings.Contains(err.Error(), "link MTU is 1400") {
		t.Fatalf("got %v", err)
	}
}

func TestMeshJoinCheckPreviewWithoutAHost(t *testing.T) {
	out, err := MeshJoinCheck{}.Preview(context.Background(), &stage.Host{})
	if err != nil || !strings.Contains(out, "changes nothing") {
		t.Fatalf("%q, %v", out, err)
	}
}

// A ping of this payload is one packet of the size the tunnel needs.
func TestLinkProbeSendsTheTunnelSize(t *testing.T) {
	if !strings.Contains(linkProbeCmd, "-M do -s 1432 1.1.1.1") {
		t.Errorf("probe:\n%s", linkProbeCmd)
	}
}
