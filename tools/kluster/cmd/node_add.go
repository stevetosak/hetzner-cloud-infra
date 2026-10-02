package cmd

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/cluster"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/pipeline"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/remote"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/terraform"
)

var (
	nodeAddServerType string
	nodeAddPrivateIP  string
	nodeAddDryRun     bool
)

var nodeAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Provision and bootstrap a single new worker node",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runNodeAdd(cmd.Context(), args[0], &pipeline.RunOptions{
			DryRun: nodeAddDryRun,
			Logger: logger,
		})
	},
}

func init() {
	nodeAddCmd.Flags().StringVar(&nodeAddServerType, "server-type", "cx23", "Hetzner server type for the new node")
	nodeAddCmd.Flags().StringVar(&nodeAddPrivateIP, "private-ip", "", "private IP on the worker subnet (auto-assigned if omitted)")
	nodeAddCmd.Flags().BoolVar(&nodeAddDryRun, "dry-run", false, "print commands without executing them")
	nodeCmd.AddCommand(nodeAddCmd)
}

func runNodeAdd(ctx context.Context, name string, opts *pipeline.RunOptions) error {
	path := tfvarsPath()

	workers, err := terraform.ReadWorkers(path)
	if err != nil {
		return err
	}
	if _, exists := workers[name]; exists {
		return fmt.Errorf("worker %q already exists in %s", name, path)
	}

	privateIP := nodeAddPrivateIP
	if privateIP == "" {
		var err error
		privateIP, err = nextAvailablePrivateIP(workers)
		if err != nil {
			return err
		}
	}
	opts.Logger.Info("adding worker", "name", name, "serverType", nodeAddServerType, "privateIP", privateIP)

	if opts.DryRun {
		opts.Logger.Info("[DRY-RUN] would add worker to terraform.tfvars", "name", name)
	} else if err := terraform.AddWorker(path, name, terraform.Worker{
		PrivateIP:  privateIP,
		ServerType: nodeAddServerType,
		Labels:     map[string]string{"role": "worker"},
	}); err != nil {
		return err
	}

	tf, err := terraformClient()
	if err != nil {
		return err
	}

	nodeSuffix := nodeSuffixNow()
	if opts.DryRun {
		opts.Logger.Info("[DRY-RUN] would run terraform apply", "vars", terraformApplyVars(nodeSuffix))
	} else {
		opts.Logger.Info("applying terraform to provision the new server")
		if err := tf.Apply(ctx, terraformApplyVars(nodeSuffix)); err != nil {
			// Roll back the tfvars entry so a retry doesn't fail with
			// "worker already exists" against a server that was never
			// actually created.
			if rollbackErr := terraform.RemoveWorker(path, name); rollbackErr != nil {
				return fmt.Errorf("terraform apply failed (%w) AND rolling back %s failed (%v) — remove %q from it by hand before retrying", err, path, rollbackErr, name)
			}
			return fmt.Errorf("terraform apply failed, rolled back %s: %w", path, err)
		}
	}

	publicIP, err := resolveNewWorkerPublicIP(ctx, tf, name, opts)
	if err != nil {
		return err
	}

	// --dry-run must never touch the network: everything below that would
	// otherwise dial SSH is gated so nothing but the tfvars/terraform-output
	// reads above ever runs for real.
	var cp, worker *remote.Client
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
	}

	vpnIP, err := allocateVpnIP(ctx, cp, opts)
	if err != nil {
		return err
	}
	opts.Logger.Info("allocated VPN IP", "vpnIP", vpnIP)

	if !opts.DryRun {
		joinCmd, err = cluster.CreateJoinToken(ctx, cp)
		if err != nil {
			return err
		}

		worker, err = dialSSH(publicIP, cfg.Node.SSHUser)
		if err != nil {
			return fmt.Errorf("dialing new worker %s: %w", publicIP, err)
		}
		defer worker.Close()
	}

	node := &pipeline.NodeContext{
		Name:            name,
		PublicIP:        publicIP,
		PrivateIP:       privateIP,
		VpnIP:           vpnIP,
		SSH:             worker,
		CPWgPublicKey:   cpPubKey,
		CPPublicIP:      cfg.ControlPlane.PublicIP,
		KubeJoinCommand: joinCmd,
		Cfg:             cfg,
	}

	if err := pipeline.RunOnNode(ctx, pipeline.DefaultPipeline(), node, opts); err != nil {
		return fmt.Errorf("bootstrapping %s: %w", name, err)
	}

	if err := addPeerToControlPlane(ctx, cp, worker, node, opts); err != nil {
		return err
	}

	validateVpnConnectivity([]*pipeline.NodeContext{node}, opts)

	if opts.DryRun {
		opts.Logger.Info("[DRY-RUN] would run longhorn preflight")
	} else if _, err := cluster.LonghornPreflight(ctx, cp); err != nil {
		return fmt.Errorf("longhorn preflight: %w", err)
	}

	opts.Logger.Info("node added", "name", name, "publicIP", publicIP, "vpnIP", vpnIP)
	return nil
}

// nextAvailablePrivateIP picks the next private IP after the highest one
// already assigned to a worker (10.0.2.6, .7, .8 -> .9), rather than
// filling the lowest gap: Hetzner Cloud reserves the first address in a
// subnet for the gateway, and the existing workers already leave .1-.5
// unused, so continuing the established sequence is safer than scanning
// from the bottom of the range. Falls back to .6 (the historical starting
// point in terraform.tfvars) when no workers exist yet.
func nextAvailablePrivateIP(workers map[string]terraform.Worker) (string, error) {
	const base = "10.0.2"
	highest := 5 // one below the historical starting point, .6
	for _, w := range workers {
		ip := net.ParseIP(w.PrivateIP)
		if ip == nil || !strings.HasPrefix(w.PrivateIP, base+".") {
			continue
		}
		last := ip.To4()[3]
		if int(last) > highest {
			highest = int(last)
		}
	}
	next := highest + 1
	if next > 254 {
		return "", fmt.Errorf("no room left in %s.0/24 for another worker private IP", base)
	}
	return fmt.Sprintf("%s.%d", base, next), nil
}

// addPeerToControlPlane appends the newly-bootstrapped node's [Peer] block
// to the control plane's wg0.conf and hot-reloads it via `wg syncconf`,
// rather than the full-rebuild+restart bootstrap uses — this must not
// disrupt already-connected workers.
func addPeerToControlPlane(ctx context.Context, cp, worker *remote.Client, node *pipeline.NodeContext, opts *pipeline.RunOptions) error {
	if opts.DryRun {
		opts.Logger.Info("[DRY-RUN] would append peer block to control plane wg0.conf and run wg syncconf")
		return nil
	}

	workerPubKey, err := worker.ReadFile(ctx, "/etc/wireguard/public.key")
	if err != nil {
		return fmt.Errorf("fetching new worker's WireGuard public key: %w", err)
	}

	existing, err := cp.ReadFile(ctx, "/etc/wireguard/wg0.conf")
	if err != nil {
		return fmt.Errorf("reading control plane wg0.conf: %w", err)
	}

	updated := cluster.AppendPeerBlock(existing, node.Name, workerPubKey, node.VpnIP+"/32")

	// Process substitution requires bash; the SSH session's login shell
	// isn't guaranteed to be bash, so invoke it explicitly.
	if err := installWgConf(ctx, cp, updated, `bash -c "wg syncconf wg0 <(wg-quick strip wg0)"`); err != nil {
		return err
	}

	opts.Logger.Info("added peer to control plane WireGuard config", "node", node.Name)
	return nil
}

// resolveNewWorkerPublicIP finds the public IP of the server terraform just
// created for name. It reuses liveNamesByTfvarsKey (also used by node_list
// and node_remove) for the tfvars-key-to-live-name prefix match, rather
// than a second hand-rolled copy of that rule.
func resolveNewWorkerPublicIP(ctx context.Context, tf *terraform.Client, name string, opts *pipeline.RunOptions) (string, error) {
	if opts.DryRun {
		return "<dry-run-public-ip>", nil
	}

	names, err := tf.WorkerNames(ctx)
	if err != nil {
		return "", err
	}
	ips, err := tf.WorkerPublicIPs(ctx)
	if err != nil {
		return "", err
	}

	live := liveNamesByTfvarsKey(map[string]terraform.Worker{name: {}}, names)
	liveName, ok := live[name]
	if !ok {
		return "", fmt.Errorf("could not find a live server matching %q in terraform output worker_names %v", name, names)
	}
	for i, n := range names {
		if n == liveName {
			return ips[i], nil
		}
	}
	return "", fmt.Errorf("internal error: resolved live name %q not found in worker_names %v", liveName, names)
}

// allocateVpnIP scans the control plane's current wg0.conf for taken
// addresses and returns the lowest free one, unlike bootstrap's positional
// assignment: node add must not disturb already-running workers' IPs.
func allocateVpnIP(ctx context.Context, cp *remote.Client, opts *pipeline.RunOptions) (string, error) {
	if opts.DryRun {
		return "<dry-run-vpn-ip>", nil
	}

	wgConf, err := cp.ReadFile(ctx, "/etc/wireguard/wg0.conf")
	if err != nil {
		return "", fmt.Errorf("reading control plane wg0.conf: %w", err)
	}

	excluded := append([]string{cfg.ControlPlane.VpnIP}, cluster.ExtractPeerIPs(wgConf)...)
	excluded = append(excluded, staticPeerIPs()...)

	return cluster.NextAvailableVpnIP(cfg.WireGuard.Subnet, excluded)
}
