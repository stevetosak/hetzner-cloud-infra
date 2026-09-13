package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/cluster"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/remote"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/terraform"
)

// installWgConf writes newConf to the control plane's wg0.conf via a
// write-new-then-backup-then-move sequence — it never overwrites the live
// file directly, so a write or reload failure always leaves either the old
// config or a clean new one in place, never a partial one — then reloads
// WireGuard using reloadCmd (a full restart for the bootstrap/reset path,
// `wg syncconf` for the incremental node add/remove path).
func installWgConf(ctx context.Context, cp *remote.Client, newConf, reloadCmd string) error {
	if err := cp.WriteFile(ctx, "/etc/wireguard/wg0.conf.new", newConf, 0o600); err != nil {
		return fmt.Errorf("writing new wg0.conf: %w", err)
	}
	cmd := fmt.Sprintf("cp /etc/wireguard/wg0.conf /etc/wireguard/wg0.conf.backup && mv /etc/wireguard/wg0.conf.new /etc/wireguard/wg0.conf && %s", reloadCmd)
	if _, err := cp.Run(ctx, cmd); err != nil {
		return fmt.Errorf("installing new wg0.conf: %w", err)
	}
	return nil
}

// nodeSuffixNow returns a timestamp suitable for terraform's node_suffix
// variable, matching the format reset-nodes.sh already uses
// (`date +%Y%m%d-%H%M%S`).
func nodeSuffixNow() string {
	return time.Now().UTC().Format("20060102-150405")
}

// dialSSH opens an SSH connection to host as user, using the --ssh-key flag
// if set or the running ssh-agent otherwise.
func dialSSH(host, user string) (*remote.Client, error) {
	return remote.Dial(host, user, sshKeyPath)
}

// terraformClient returns a Terraform client rooted at the configured
// infra/ working directory.
func terraformClient() (*terraform.Client, error) {
	return terraform.NewClient(cfg.Terraform.WorkDir)
}

// tfvarsPath is the terraform.tfvars file kluster reads/writes as the
// single source of truth for worker nodes.
func tfvarsPath() string {
	return filepath.Join(cfg.Terraform.WorkDir, "terraform.tfvars")
}

// staticPeers converts kluster.yaml's wireguard.peers into the shape
// cluster.RebuildCPWgConfig/AppendPeerBlock expect.
func staticPeers() []cluster.StaticPeer {
	peers := make([]cluster.StaticPeer, len(cfg.WireGuard.Peers))
	for i, p := range cfg.WireGuard.Peers {
		peers[i] = cluster.StaticPeer{Name: p.Name, PublicKey: p.PublicKey, AllowedIPs: p.AllowedIPs}
	}
	return peers
}

// staticPeerIPs returns the bare IPs (mask stripped) of every static peer,
// for excluding them from VPN IP allocation.
func staticPeerIPs() []string {
	ips := make([]string, len(cfg.WireGuard.Peers))
	for i, p := range cfg.WireGuard.Peers {
		ips[i] = p.AllowedIPs
	}
	return ips
}

// terraformApplyVars are the -var flags reset-nodes.sh already passes today:
// open SSH to the operator's current public IP, and force fresh server
// names so a taint+recreate doesn't collide with the old name.
func terraformApplyVars(nodeSuffix string) map[string]string {
	return map[string]string{
		"allow_public_ssh": "true",
		"node_suffix":      nodeSuffix,
	}
}
