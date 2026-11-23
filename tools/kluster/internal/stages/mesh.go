package stages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/local"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/mesh"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/nodeset"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

const (
	meshRunbook = "docs/runbook/workers.md#join-the-mesh"
	meshConf    = "/etc/wireguard/" + mesh.Interface + ".conf"
	meshKeyFile = "/etc/wireguard/mesh.key"
	meshPubFile = "/etc/wireguard/mesh.pub"
	meshUnit    = "wg-quick@" + mesh.Interface
)

// MeshKey makes the machine's mesh key on the machine. Only the public half
// ever leaves it, and a second run keeps the key it finds.
type MeshKey struct{}

func (MeshKey) Name() string    { return "mesh-key" }
func (MeshKey) Runbook() string { return meshRunbook }

func (MeshKey) script() string {
	return `apt-get -o DPkg::Lock::Timeout=300 install -y wireguard
mkdir -p /etc/wireguard
umask 077
[ -s ` + meshKeyFile + ` ] || wg genkey > ` + meshKeyFile + `
chmod 600 ` + meshKeyFile + `
wg pubkey < ` + meshKeyFile + ` > ` + meshPubFile + `
`
}

func (MeshKey) checks() []check {
	return []check{
		{"wireguard-tools", "command -v wg && command -v wg-quick"},
		{"private-key", `[ -s ` + meshKeyFile + ` ] && [ "$(stat -c %a ` + meshKeyFile + `)" = 600 ]`},
		{"public-key", `[ "$(wg pubkey < ` + meshKeyFile + `)" = "$(cat ` + meshPubFile + `)" ]`},
	}
}

func (k MeshKey) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	return probe(ctx, h, k.checks())
}

func (k MeshKey) Preview(context.Context, *stage.Host) (string, error) { return k.script(), nil }

func (k MeshKey) Act(ctx context.Context, h *stage.Host) error {
	_, err := runScript(ctx, h, k.Name(), k.script())
	return err
}

// MeshPublicKey reads the public key MeshKey left on the machine.
func MeshPublicKey(ctx context.Context, h *stage.Host) (string, error) {
	out, err := h.Exec.Run(ctx, "cat "+meshPubFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// WireGuardMesh writes the machine's wg-mesh config and brings the running
// interface to it. `wg syncconf` keeps live tunnels up but does not touch
// routes, so the Stage reconciles those itself.
type WireGuardMesh struct {
	Config mesh.Config
}

func (WireGuardMesh) Name() string    { return "wireguard-mesh" }
func (WireGuardMesh) Runbook() string { return meshRunbook }

func (w WireGuardMesh) script() string {
	address := local.Quote(w.Config.Address.String())
	var b strings.Builder
	// A failed $(cat …) inside a heredoc does not stop the script, and would
	// write a config with an empty key.
	b.WriteString("test -s " + meshKeyFile + "\numask 077\n")
	// An unquoted heredoc, so $(cat …) puts the key in on the host.
	b.WriteString("cat > " + meshConf + " <<EOF\n" + w.Config.Text("$(cat "+meshKeyFile+")") + "EOF\n")
	b.WriteString("chmod 600 " + meshConf + "\n\nsystemctl enable " + meshUnit + "\n")
	b.WriteString("if systemctl is-active --quiet " + meshUnit + " && ip -4 -o addr show dev " + mesh.Interface + " | grep -qwF -- " + address + "; then\n")
	b.WriteString("  wg syncconf " + mesh.Interface + " <(wg-quick strip " + mesh.Interface + ")\nelse\n  systemctl restart " + meshUnit + "\nfi\n\n")
	b.WriteString(w.routeScript() + "\n" + w.forwardScript())
	return b.String()
}

// routeScript adds each wanted route and deletes every other route of the
// interface. `ip route` prints a host route without its /32.
func (w WireGuardMesh) routeScript() string {
	return "want=" + local.Quote(strings.Join(w.Config.Routes(), " ")) + `
for route in $want; do ip -4 route replace "$route" dev ` + mesh.Interface + `; done
ip -4 route show dev ` + mesh.Interface + ` | awk '{print $1}' | while read -r route; do
  case "$route" in */*) ;; *) route="$route/32" ;; esac
  case " $want " in *" $route "*) ;; *) ip -4 route del "$route" dev ` + mesh.Interface + ` ;; esac
done
`
}

// forwardScript sets the relay's rule, and removes it from a machine that is
// no longer the relay.
func (w WireGuardMesh) forwardScript() string {
	if w.Config.Forward {
		return "sysctl -qw net.ipv4.ip_forward=1\niptables -C FORWARD " + mesh.ForwardMatch + " 2>/dev/null || iptables -I FORWARD 1 " + mesh.ForwardMatch + "\n"
	}
	return "while iptables -C FORWARD " + mesh.ForwardMatch + " 2>/dev/null; do iptables -D FORWARD " + mesh.ForwardMatch + "; done\n"
}

// confHashCmd prints the hash of the config at path without its PrivateKey
// line, so the Probe compares the file and no key leaves the host.
func confHashCmd(path string) string {
	return "grep -v '^PrivateKey = ' " + path + " | sha256sum | cut -d' ' -f1"
}

func (w WireGuardMesh) checks() []check {
	sum := sha256.Sum256([]byte(w.Config.Text("")))
	var keys []string
	for _, p := range w.Config.Peers {
		keys = append(keys, p.PublicKey)
	}
	slices.Sort(keys)
	forward := "! iptables -C FORWARD " + mesh.ForwardMatch
	if w.Config.Forward {
		forward = `[ "$(sysctl -n net.ipv4.ip_forward)" = 1 ] && iptables -C FORWARD ` + mesh.ForwardMatch
	}
	c := []check{
		{"conf", `[ "$(` + confHashCmd(meshConf) + `)" = ` + hex.EncodeToString(sum[:]) + " ]"},
		{"conf-key", "grep -q '^PrivateKey = .' " + meshConf},
		{"up", "systemctl is-active --quiet " + meshUnit + " && systemctl is-enabled --quiet " + meshUnit},
		{"live-key", `[ "$(wg show ` + mesh.Interface + ` public-key)" = "$(cat ` + meshPubFile + `)" ]`},
		{"address", "ip -4 -o addr show dev " + mesh.Interface + " | grep -qwF -- " + local.Quote(w.Config.Address.String())},
		{"mtu", `[ "$(cat /sys/class/net/` + mesh.Interface + `/mtu)" = ` + strconv.Itoa(w.Config.MTU) + " ]"},
		{"peers", `[ "$(wg show ` + mesh.Interface + ` peers | LC_ALL=C sort | xargs)" = ` + local.Quote(strings.Join(keys, " ")) + " ]"},
		{"routes", `[ "$(ip -4 route show dev ` + mesh.Interface + ` | awk '{print ($1 ~ /\//) ? $1 : $1 "/32"}' | LC_ALL=C sort | xargs)" = ` +
			local.Quote(strings.Join(w.Config.Routes(), " ")) + " ]"},
		{"forward", forward},
	}
	for _, p := range w.Config.Peers {
		c = append(c, check{"peer-" + p.Name, peerCheck(p)})
	}
	return c
}

// peerCheck compares the addresses the running interface gives one peer.
func peerCheck(p mesh.Peer) string {
	allowed := make([]string, len(p.AllowedIPs))
	for i, a := range p.AllowedIPs {
		allowed[i] = a.String()
	}
	slices.Sort(allowed)
	return `[ "$(wg show ` + mesh.Interface + ` allowed-ips | grep -F -- ` + local.Quote(p.PublicKey) +
		` | tr -s '\t ' '\n' | tail -n +2 | LC_ALL=C sort | xargs)" = ` + local.Quote(strings.Join(allowed, " ")) + " ]"
}

func (w WireGuardMesh) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	return probe(ctx, h, w.checks())
}

func (w WireGuardMesh) Preview(context.Context, *stage.Host) (string, error) { return w.script(), nil }

func (w WireGuardMesh) Act(ctx context.Context, h *stage.Host) error {
	_, err := runScript(ctx, h, w.Name(), w.script())
	return err
}

// The join check's limits. WireGuard over IPv4 adds 60 bytes to the tunnel's
// MTU, and a ping adds 28 to its payload.
const (
	minLinkMTU    = mesh.MTU + 60
	pingPayload   = minLinkMTU - 28
	mtuProbeHost  = "1.1.1.1"
	addressesRead = "ip -4 -o addr show"
)

// clusterRanges are the networks a machine's LAN must not overlap: a packet
// for one of them would stay on the LAN.
var clusterRanges = []struct {
	name   string
	prefix netip.Prefix
}{
	{"the private network", privateNetworkRange},
	{"the pods", netip.MustParsePrefix("10.244.0.0/16")},
	{"the services", netip.MustParsePrefix("10.96.0.0/16")},
	{"the VPN", netip.MustParsePrefix("10.100.0.0/24")},
	{"the mesh", nodeset.MeshRange},
	{"the range kept for a move of the pods to Cilium", netip.MustParsePrefix("10.245.0.0/16")},
}

var privateNetworkRange = netip.MustParsePrefix("10.0.0.0/16")

// clusterInterfaces carry the cluster's own ranges by design.
var clusterInterfaces = []string{"lo", "wg0", mesh.Interface, "flannel.1", "cni0"}

// MeshJoinCheck refuses a machine the mesh cannot work on: a LAN that overlaps
// a cluster range, or a link too small for the tunnel. It only reads.
type MeshJoinCheck struct {
	// Hcloud is set on a Hetzner machine, whose private interface is meant
	// to stand in the private network.
	Hcloud bool
}

func (MeshJoinCheck) Name() string    { return "mesh-join-check" }
func (MeshJoinCheck) Runbook() string { return meshRunbook }

// linkProbeCmd reports the default route's device and MTU, and whether a
// full-size packet with Don't Fragment set reaches an outside address. The
// Hetzner firewalls close inbound ICMP, so the probe cannot ping another node.
var linkProbeCmd = fmt.Sprintf(`dev=$(ip -4 route show default | awk '{print $5; exit}')
echo "mtu=$(cat "/sys/class/net/$dev/mtu" 2>/dev/null)"
ping -n -q -c 3 -W 2 -M do -s %[1]d %[2]s >/dev/null 2>&1 && echo large=ok || echo large=lost
ping -n -q -c 3 -W 2 %[2]s >/dev/null 2>&1 && echo small=ok || echo small=lost
`, pingPayload, mtuProbeHost)

// lanOverlaps names each address of `ip -4 -o addr show` that overlaps a
// cluster range.
func lanOverlaps(addresses string, hcloud bool) ([]string, error) {
	var faults []string
	for _, line := range strings.Split(addresses, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[2] != "inet" {
			continue
		}
		dev, _, _ := strings.Cut(f[1], "@")
		if slices.Contains(clusterInterfaces, dev) {
			continue
		}
		lan, err := netip.ParsePrefix(f[3])
		if err != nil {
			return nil, fmt.Errorf("reading the address of %s: %w", dev, err)
		}
		for _, r := range clusterRanges {
			if hcloud && r.prefix == privateNetworkRange {
				continue
			}
			if lan.Overlaps(r.prefix) {
				faults = append(faults, fmt.Sprintf("%s has %s, which overlaps %s (%s)", dev, lan.Masked(), r.name, r.prefix))
			}
		}
	}
	return faults, nil
}

// linkFault reads linkProbeCmd's output. When even the small ping is lost the
// path cannot be measured, so only the interface MTU decides.
func linkFault(out string) (fault, detail string) {
	facts := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			facts[k] = v
		}
	}
	mtu, err := strconv.Atoi(facts["mtu"])
	switch {
	case err != nil:
		return "the MTU of the default route's device cannot be read", ""
	case mtu < minLinkMTU:
		return fmt.Sprintf("the link MTU is %d, and the mesh needs at least %d", mtu, minLinkMTU), ""
	case facts["large"] == "ok":
		return "", fmt.Sprintf("link MTU %d, path holds %d", mtu, minLinkMTU)
	case facts["small"] == "ok":
		return fmt.Sprintf("the path to %s drops a packet of %d bytes, and the mesh needs that size", mtuProbeHost, minLinkMTU), ""
	}
	return "", fmt.Sprintf("link MTU %d, path not measured: %s does not answer", mtu, mtuProbeHost)
}

// faults runs the checks on the machine.
func (c MeshJoinCheck) faults(ctx context.Context, h *stage.Host) (faults []string, detail string, err error) {
	addresses, err := h.Exec.Run(ctx, addressesRead)
	if err != nil {
		return nil, "", err
	}
	if faults, err = lanOverlaps(addresses, c.Hcloud); err != nil {
		return nil, "", err
	}
	out, err := h.Exec.Run(ctx, linkProbeCmd)
	if err != nil {
		return nil, "", err
	}
	fault, detail := linkFault(out)
	if fault != "" {
		faults = append(faults, fault)
	}
	return faults, detail, nil
}

func (c MeshJoinCheck) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	faults, detail, err := c.faults(ctx, h)
	if err != nil {
		return stage.Status{}, err
	}
	if len(faults) > 0 {
		return stage.Status{Detail: "refused: " + strings.Join(faults, "; ")}, nil
	}
	return stage.Status{Done: true, Detail: "no LAN overlap, " + detail}, nil
}

func (c MeshJoinCheck) Preview(ctx context.Context, h *stage.Host) (string, error) {
	if h.Exec == nil {
		return "# reads the machine's addresses and measures its link; changes nothing\n", nil
	}
	faults, _, err := c.faults(ctx, h)
	if err != nil {
		return "", err
	}
	return "# refuses the join: " + strings.Join(faults, "; ") + "\n", nil
}

// Act changes nothing: a machine that fails the check needs the operator.
func (c MeshJoinCheck) Act(ctx context.Context, h *stage.Host) error {
	faults, _, err := c.faults(ctx, h)
	if err != nil {
		return err
	}
	if len(faults) > 0 {
		return errors.New("the machine cannot join the mesh: " + strings.Join(faults, "; "))
	}
	return nil
}
