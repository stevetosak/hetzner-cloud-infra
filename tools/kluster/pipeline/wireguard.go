package pipeline

import (
	"context"
	"fmt"
)

// persistentKeepalive matches bootstrap_node-2-wireguard.sh's hardcoded
// value; it's a WireGuard NAT-traversal tuning constant, not something an
// operator needs to change per-deployment, so it isn't exposed in
// kluster.yaml.
const persistentKeepalive = 25

const wgDir = "/etc/wireguard"

// WireGuardStage installs WireGuard, generates a keypair on the node, and
// configures wg0 to peer with the control plane.
// Ports infra/scripts/bootstrap/pipeline/bootstrap_node-2-wireguard.sh.
type WireGuardStage struct{}

func (WireGuardStage) Name() string { return "wireguard" }

func (WireGuardStage) Run(ctx context.Context, node *NodeContext, opts *RunOptions) error {
	if node.CPWgPublicKey == "" || node.VpnIP == "" || node.CPPublicIP == "" {
		return fmt.Errorf("wireguard stage requires CPWgPublicKey, VpnIP, and CPPublicIP set on NodeContext")
	}

	if err := runCmd(ctx, node, opts, "apt-get update && apt-get install -y wireguard"); err != nil {
		return fmt.Errorf("installing wireguard: %w", err)
	}
	if err := runCmd(ctx, node, opts, "mkdir -p "+wgDir); err != nil {
		return fmt.Errorf("creating wireguard dir: %w", err)
	}
	genKeyCmd := fmt.Sprintf("wg genkey | tee %s/private.key | wg pubkey | tee %s/public.key", wgDir, wgDir)
	if err := runCmd(ctx, node, opts, genKeyCmd); err != nil {
		return fmt.Errorf("generating wireguard keys: %w", err)
	}
	check(opts, node, "WireGuard installed and keys generated")

	var privateKey string
	if opts.DryRun {
		privateKey = "<dry-run-private-key>"
	} else {
		key, err := node.SSH.ReadFile(ctx, wgDir+"/private.key")
		if err != nil {
			return fmt.Errorf("reading generated private key: %w", err)
		}
		privateKey = key
	}

	wgConf := fmt.Sprintf(
		"[Interface]\nAddress = %s/24\nPrivateKey = %s\n\n[Peer]\nPublicKey = %s\nAllowedIps = %s\nEndpoint = %s:%d\nPersistentKeepalive = %d\n",
		node.VpnIP, privateKey, node.CPWgPublicKey, node.Cfg.WireGuard.Subnet, node.CPPublicIP, node.Cfg.WireGuard.Port, persistentKeepalive,
	)
	if err := writeFile(ctx, node, opts, wgDir+"/wg0.conf", wgConf, 0o600); err != nil {
		return fmt.Errorf("writing wg0.conf: %w", err)
	}
	check(opts, node, "WireGuard wg0.conf created")

	if err := runCmd(ctx, node, opts, "systemctl enable wg-quick@wg0"); err != nil {
		return fmt.Errorf("enabling wg-quick@wg0: %w", err)
	}
	if err := runCmd(ctx, node, opts, "systemctl start wg-quick@wg0"); err != nil {
		return fmt.Errorf("starting wg-quick@wg0: %w", err)
	}
	check(opts, node, "WireGuard started")

	return nil
}
