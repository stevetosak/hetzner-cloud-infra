package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/nodelist"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/remote"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/sshgate"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

func init() {
	nodeCmd.AddCommand(nodeListCmd)
	rootCmd.AddCommand(nodeCmd)
}

var nodeCmd = &cobra.Command{
	Use:   "node",
	Short: "The Workers (docs/runbook/workers.md)",
}

var nodeListCmd = &cobra.Command{
	Use:   "list",
	Short: "The Worker set, the Hetzner servers and the Nodes side by side, drift marked",
	Long: "Read-only. The Nodes are read with kubectl on the Control Plane. In a rehearsal that needs " +
		"bootstrap SSH on the Control Plane firewall; node list never opens it, so with it closed the " +
		"Node column shows ?.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, nodeList)
	},
}

func nodeList(ctx context.Context, a *app) error {
	path, err := a.workerSetPath()
	if err != nil {
		return err
	}
	set, err := workerset.Load(path)
	if err != nil {
		return err
	}
	servers, err := a.api.Servers(ctx)
	if err != nil {
		return err
	}
	nodes, nodesErr := a.readNodes(ctx)
	in := nodelist.Input{
		Set:          set.Workers(),
		Servers:      servers,
		Nodes:        nodes,
		NodesRead:    nodesErr == nil,
		ControlPlane: a.cfg.ControlPlane.Name,
	}
	rows := nodelist.Compare(in)
	fmt.Fprintf(a.out, "Worker set: %s\n", path)
	if err := nodelist.Print(a.out, rows, in.NodesRead); err != nil {
		return err
	}
	if nodesErr != nil {
		return fmt.Errorf("Nodes not read: %w", nodesErr)
	}
	if nodelist.HasDrift(rows) {
		fmt.Fprintln(a.out, "drift: the records disagree (⚠ above)")
	} else {
		fmt.Fprintln(a.out, "no drift")
	}
	return nil
}

// readNodes reads the Nodes on the Control Plane. In a rehearsal with
// bootstrap SSH closed it reads nothing and says why.
func (a *app) readNodes(ctx context.Context) ([]kube.Node, error) {
	if !a.env.IsLive() {
		open, err := a.api.SSHOpen(ctx, sshgate.ControlPlane.Name)
		if err != nil {
			return nil, err
		}
		if !open {
			return nil, errors.New("bootstrap SSH on " + sshgate.ControlPlane.Name + " is closed (kluster --env rehearsal ssh open)")
		}
	}
	h, done, err := a.dialControlPlane(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return kube.Kubectl{Exec: h.Exec}.Nodes(ctx)
}

// dialControlPlane reaches the Control Plane for a Worker command, verified
// only by kluster's pins. A rehearsal logs in as root on its public address,
// through bootstrap SSH the caller opened: its kubeconfig and VPN addresses
// are the live ones, which the laptop's WireGuard routes to the LIVE hub.
// Live logs in as the operator on the VPN address, through that WireGuard,
// with sudo; its pin comes from `kluster pin import` once.
func (a *app) dialControlPlane(ctx context.Context) (*stage.Host, func(), error) {
	cp := a.cfg.ControlPlane
	addr, user, sudo := cp.VpnIP, cp.SSHUser, true
	if !a.env.IsLive() {
		srv, err := a.api.Server(ctx, cp.Name)
		if err != nil {
			return nil, nil, err
		}
		if srv == nil {
			return nil, nil, fmt.Errorf("the project has no %s: run kluster cp init first", cp.Name)
		}
		addr, user, sudo = srv.PublicIP, "root", false
	}
	if ok, err := a.pins.Has(addr); err != nil {
		return nil, nil, err
	} else if !ok {
		return nil, nil, fmt.Errorf("no pin for %s at %s: kluster trusts only its pins (live: kluster pin import %s)", cp.Name, addr, cp.VpnIP)
	}
	cb, err := a.pins.Callback()
	if err != nil {
		return nil, nil, err
	}
	auth, err := remote.Auth(a.cfg.SSH.IdentityFile)
	if err != nil {
		return nil, nil, err
	}
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c, err := remote.Dial(wait, addr, remote.Config{User: user, Auth: auth, HostKeyCallback: cb, Sudo: sudo})
	if err != nil {
		return nil, nil, fmt.Errorf("login to %s: %w", cp.Name, err)
	}
	fmt.Fprintf(a.out, "[%s] login as %s at %s verified against the pinned host key\n", cp.Name, user, addr)
	return &stage.Host{Name: cp.Name, Addr: addr, Exec: c}, func() { _ = c.Close() }, nil
}
