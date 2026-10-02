package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"time"

	"github.com/spf13/cobra"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/cluster"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/pipeline"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/remote"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/terraform"
)

var (
	bootstrapInteractive bool
	bootstrapDryRun      bool
)

var bootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Bootstrap every worker currently in terraform.tfvars, in parallel",
	Long: "Reads the live worker list from Terraform outputs, runs the full 5-stage " +
		"pipeline on every worker in parallel, rebuilds the control plane's WireGuard " +
		"config from scratch, and runs the Longhorn preflight check. " +
		"Ports infra/scripts/bootstrap/bootstrap_workers.sh.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBootstrap(cmd.Context(), &pipeline.RunOptions{
			DryRun:      bootstrapDryRun,
			Interactive: bootstrapInteractive,
			Logger:      logger,
		})
	},
}

func init() {
	bootstrapCmd.Flags().BoolVar(&bootstrapInteractive, "interactive", false, "pause for confirmation between stages")
	bootstrapCmd.Flags().BoolVar(&bootstrapDryRun, "dry-run", false, "print commands without executing them")
	rootCmd.AddCommand(bootstrapCmd)
}

func runBootstrap(ctx context.Context, opts *pipeline.RunOptions) error {
	tf, err := terraformClient()
	if err != nil {
		return err
	}

	publicIPs, err := tf.WorkerPublicIPs(ctx)
	if err != nil {
		return fmt.Errorf("reading worker public IPs: %w", err)
	}
	if len(publicIPs) == 0 {
		return fmt.Errorf("no worker nodes found in terraform state")
	}
	opts.Logger.Info("found worker nodes", "count", len(publicIPs))

	// This correlation is load-bearing, not cosmetic: the returned names
	// become each node's WireGuard peer comment, and `node remove` looks
	// peers up by that same tfvars-key comment later. Falling back to a
	// different identity scheme here (e.g. the public IP) on error would
	// silently make every peer added by this bootstrap unremovable by
	// `node remove <tfvars-key>` — so a correlation failure is fatal.
	names, privateIPs, err := workerTfvarsInfo(ctx, tf)
	if err != nil {
		return fmt.Errorf("correlating tfvars entries with live workers: %w", err)
	}

	// --dry-run must never touch the network: everything past this point
	// that would otherwise dial SSH or call terraform apply is gated so
	// only the read-only `terraform output` calls above ever run for real.
	var cp *remote.Client
	cpPubKey := "<dry-run-pubkey>"
	joinCmd := "<dry-run-join-command>"
	if !opts.DryRun {
		cp, err = dialSSH(cfg.ControlPlane.VpnIP, cfg.ControlPlane.SSHUser)
		if err != nil {
			return fmt.Errorf("dialing control plane at %s: %w", cfg.ControlPlane.VpnIP, err)
		}
		defer cp.Close()

		cpPubKey, err = cp.ReadFile(ctx, "/etc/wireguard/public.key")
		if err != nil {
			return fmt.Errorf("fetching control plane WireGuard public key: %w", err)
		}
		opts.Logger.Info("fetched control plane WireGuard public key")

		joinCmd, err = cluster.CreateJoinToken(ctx, cp)
		if err != nil {
			return err
		}
	}

	nodes := make([]*pipeline.NodeContext, len(publicIPs))
	conns := make([]*remote.Client, len(publicIPs))
	for i, ip := range publicIPs {
		var conn *remote.Client
		if !opts.DryRun {
			conn, err = dialSSH(ip, cfg.Node.SSHUser)
			if err != nil {
				return fmt.Errorf("dialing worker %s: %w", ip, err)
			}
			conns[i] = conn
		}
		nodes[i] = &pipeline.NodeContext{
			Name:            names[i],
			PublicIP:        ip,
			PrivateIP:       privateIPs[i],
			VpnIP:           vpnIPForIndex(i),
			SSH:             conn,
			CPWgPublicKey:   cpPubKey,
			CPPublicIP:      cfg.ControlPlane.PublicIP,
			KubeJoinCommand: joinCmd,
			Cfg:             cfg,
		}
	}
	defer func() {
		for _, c := range conns {
			if c != nil {
				_ = c.Close()
			}
		}
	}()

	if err := pipeline.RunOnNodes(ctx, pipeline.DefaultPipeline(), nodes, opts); err != nil {
		return fmt.Errorf("bootstrapping workers: %w", err)
	}
	opts.Logger.Info("all workers bootstrapped")

	if err := rebuildControlPlaneWireGuard(ctx, cp, nodes, opts); err != nil {
		return err
	}

	validateVpnConnectivity(nodes, opts)

	if opts.DryRun {
		opts.Logger.Info("[DRY-RUN] would run longhorn preflight")
	} else if _, err := cluster.LonghornPreflight(ctx, cp); err != nil {
		return fmt.Errorf("longhorn preflight: %w", err)
	} else {
		opts.Logger.Info("longhorn preflight complete")
	}

	return nil
}

// vpnIPForIndex assigns VPN IPs sequentially starting at .2, matching
// bootstrap_workers.sh's VPN_INDEX scheme. A full bootstrap rebuilds every
// worker from scratch, so positional assignment (rather than the gap-aware
// scan node-add uses) is correct here and matches historical behavior.
func vpnIPForIndex(i int) string {
	return fmt.Sprintf("10.100.0.%d", i+2)
}

// workerPrivateIPsByOutputOrder correlates terraform.tfvars private_ip
// values with the worker_public_ips output order. Terraform's `for`
// expression over a map iterates in ascending key order (documented
// behavior), and tfvars keys sort the same way, so sorting tfvars keys
// reproduces the output's element order.
// workerTfvarsInfo correlates terraform.tfvars keys and private_ip values
// with the worker_public_ips/worker_names output order. Terraform's `for`
// expression over a map iterates in ascending key order (documented
// behavior), and tfvars keys sort the same way, so sorting tfvars keys
// reproduces the outputs' element order.
//
// The returned tfvars keys become each node's stable name, used as the
// WireGuard peer comment — the same stable identifier `node add` uses —
// so `node remove` can find any peer's block by tfvars key regardless of
// whether it was created by `bootstrap` or `node add`.
func workerTfvarsInfo(ctx context.Context, tf *terraform.Client) (names []string, privateIPs []string, err error) {
	workers, err := terraform.ReadWorkers(tfvarsPath())
	if err != nil {
		return nil, nil, err
	}
	liveNames, err := tf.WorkerNames(ctx)
	if err != nil {
		return nil, nil, err
	}
	keys := make([]string, 0, len(workers))
	for k := range workers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) != len(liveNames) {
		return nil, nil, fmt.Errorf("tfvars has %d workers but terraform reports %d live workers", len(keys), len(liveNames))
	}
	ips := make([]string, len(keys))
	for i, k := range keys {
		ips[i] = workers[k].PrivateIP
	}
	return keys, ips, nil
}

func rebuildControlPlaneWireGuard(ctx context.Context, cp *remote.Client, nodes []*pipeline.NodeContext, opts *pipeline.RunOptions) error {
	opts.Logger.Info("rebuilding control plane WireGuard configuration")

	workerPeers := make([]cluster.WorkerPeer, len(nodes))
	for i, n := range nodes {
		if opts.DryRun {
			workerPeers[i] = cluster.WorkerPeer{Name: n.Name, VpnIP: n.VpnIP, PublicKey: "<dry-run-pubkey>"}
			continue
		}
		pubKey, err := n.SSH.ReadFile(ctx, "/etc/wireguard/public.key")
		if err != nil {
			return fmt.Errorf("fetching WireGuard public key from %s: %w", n.Name, err)
		}
		workerPeers[i] = cluster.WorkerPeer{Name: n.Name, VpnIP: n.VpnIP, PublicKey: pubKey}
	}

	if opts.DryRun {
		opts.Logger.Info("[DRY-RUN] would rebuild control plane wg0.conf and restart wg-quick@wg0")
		return nil
	}

	cpPrivateKey, err := cp.ReadFile(ctx, "/etc/wireguard/private.key")
	if err != nil {
		return fmt.Errorf("fetching control plane WireGuard private key: %w", err)
	}

	newConf := cluster.RebuildCPWgConfig(cfg.ControlPlane.VpnIP, cpPrivateKey, cfg.WireGuard.Port, workerPeers, staticPeers())

	if err := installWgConf(ctx, cp, newConf, "systemctl restart wg-quick@wg0"); err != nil {
		return err
	}

	opts.Logger.Info("control plane wg0.conf rebuilt and WireGuard restarted")
	return nil
}

// validateVpnConnectivity pings each worker's VPN IP from the machine
// running kluster (assumed to already be a WireGuard peer, same as the
// admin laptop in bootstrap_workers.sh). Failures are logged, not fatal.
func validateVpnConnectivity(nodes []*pipeline.NodeContext, opts *pipeline.RunOptions) {
	if opts.DryRun {
		return
	}
	opts.Logger.Info("validating VPN connectivity to workers")
	for _, n := range nodes {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := exec.CommandContext(ctx, "ping", "-c", "2", "-W", "2", n.VpnIP).Run()
		cancel()
		if err != nil {
			opts.Logger.Warn("worker unreachable over VPN", "node", n.Name, "vpnIP", n.VpnIP)
		} else {
			opts.Logger.Info("worker reachable over VPN", "node", n.Name, "vpnIP", n.VpnIP, slog.String("status", "reachable"))
		}
	}
}
