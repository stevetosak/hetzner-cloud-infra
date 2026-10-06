package stages

import (
	"context"
	"fmt"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// PrivateNetwork makes sure the private interface is up with its address.
// Hetzner gives cloud-init the private interface only when the network was
// attached before the first boot reads its metadata. The hcloud provider
// attaches a fixed private IP after it creates the server, so that is a race:
// the live Control Plane got the interface on 2026-09-20, the rehearsal one on
// 2026-10-04 booted with eth0 alone and enp7s0 down. This Stage writes the
// netplan entry cloud-init would have written, matched by the MAC address the
// API reports, and brings up that one interface.
type PrivateNetwork struct {
	Interface string // enp7s0
	MAC       string // from the Hetzner API
	IP        string // 10.0.1.5
}

func (PrivateNetwork) Name() string    { return "private-network" }
func (PrivateNetwork) Runbook() string { return "docs/runbook/control-plane.md#2-base-host-setup" }

const privateNetplan = "/etc/netplan/60-kluster-private.yaml"

func (p PrivateNetwork) netplan() string {
	return fmt.Sprintf(`network:
  version: 2
  ethernets:
    %s:
      match:
        macaddress: "%s"
      dhcp4: true
      set-name: "%s"`, p.Interface, p.MAC, p.Interface)
}

func (p PrivateNetwork) script() string {
	return `# only if cloud-init did not already configure the interface
if ! grep -rqi '` + p.MAC + `' /etc/netplan/; then
  umask 077
  ` + heredoc(privateNetplan, p.netplan()) + `fi
netplan generate
networkctl reload
networkctl reconfigure ` + p.Interface + `
for i in $(seq 60); do ip -4 -o addr show ` + p.Interface + ` | grep -q 'inet ` + p.IP + `/' && break; sleep 1; done
`
}

func (p PrivateNetwork) checks() []check {
	return []check{
		{"persistent", "grep -rqi '" + p.MAC + "' /etc/netplan/"},
		{"address", "ip -4 -o addr show " + p.Interface + " | grep -q 'inet " + p.IP + "/'"},
	}
}

func (p PrivateNetwork) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	return probe(ctx, h, p.checks())
}

func (p PrivateNetwork) Preview(context.Context, *stage.Host) (string, error) { return p.script(), nil }

func (p PrivateNetwork) Act(ctx context.Context, h *stage.Host) error {
	if p.MAC == "" {
		return fmt.Errorf("no MAC address for %s from the API", p.Interface)
	}
	_, err := runScript(ctx, h, p.Name(), p.script())
	return err
}
