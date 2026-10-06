package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"sort"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/intent"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/provision"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/remote"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/sshgate"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stages"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/tf"
)

func init() {
	sshCmd.AddCommand(sshCloseCmd, sshOpenCmd)
	rehearseCmd.AddCommand(rehearseCoreCmd)
	rootCmd.AddCommand(sharedCmd, sshCmd, downCmd, rehearseCmd, versionCmd)
}

// sharedIntent is the shared Module's Intent: it may build what is missing,
// and never changes or destroys what exists.
var sharedIntent = intent.Intent{
	Description: "create what is missing in shared, change nothing that exists",
	Expectations: []intent.Expectation{
		{Address: "*", Actions: []intent.Action{intent.Create}},
	},
}

var sharedCmd = &cobra.Command{
	Use:   "shared",
	Short: "Build the shared Module: network, firewalls, SSH key, Control Plane address",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, func(ctx context.Context, a *app) error {
			m, err := a.module(ctx, config.ModuleShared)
			if err != nil {
				return err
			}
			_, _, err = tf.Converge(ctx, m, nil, sharedIntent, a.mode, a.out)
			return err
		})
	},
}

var sshCmd = &cobra.Command{
	Use:   "ssh",
	Short: "Bootstrap SSH on the cluster firewalls",
}

var sshCloseCmd = &cobra.Command{
	Use:   "close",
	Short: "Close bootstrap SSH on both firewalls and prove it over the Hetzner API",
	Long: "Every kluster run that opens port 22 closes it at its end, failed runs included. " +
		"This command is for the case where that did not happen. Runbook: " + sshgate.Runbook,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, func(ctx context.Context, a *app) error {
			g, err := a.gate(ctx)
			if err != nil {
				return err
			}
			return g.Close(ctx)
		})
	},
}

var sshOpenCmd = &cobra.Command{
	Use:   "open",
	Short: "Open bootstrap SSH on the Control Plane firewall, to inspect a rehearsal host (refused for live)",
	Long: "For looking at a rehearsal server by hand after a failed run. The open is read back over the " +
		"API; close it with `kluster --env rehearsal ssh close`, and every cp init closes it at its end anyway.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, func(ctx context.Context, a *app) error {
			if err := a.requireRehearsal("kluster ssh open"); err != nil {
				return err
			}
			g, err := a.gate(ctx)
			if err != nil {
				return err
			}
			return g.Open(ctx, sshgate.ControlPlane)
		})
	},
}

var downCmd = &cobra.Command{
	Use:   "down",
	Short: "Delete every resource in the rehearsal project (refused for live)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, func(ctx context.Context, a *app) error {
			if err := a.requireRehearsal("kluster down"); err != nil {
				return err
			}
			inv, err := a.api.Inventory(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintln(a.out, "rehearsal project:")
			inv.Print(a.out)
			if inv.Empty() || !a.mode.Apply {
				return nil
			}
			if err := a.api.DeleteAll(ctx, inv, a.out); err != nil {
				return err
			}
			after, err := a.api.Inventory(ctx)
			if err != nil {
				return err
			}
			if !after.Empty() {
				after.Print(a.out)
				return errors.New("the rehearsal project is not empty after down")
			}
			fmt.Fprintln(a.out, "read back: the rehearsal project is empty")
			return a.pins.Reset()
		})
	},
}

var rehearseCmd = &cobra.Command{
	Use:   "rehearse",
	Short: "Runs that exist only to prove kluster in the rehearsal project",
}

var rehearseCoreCmd = &cobra.Command{
	Use:   "core",
	Short: "Prove the safety core: shared from empty, SSH gate, seeded and rotated host keys",
	Long: "Builds shared, opens bootstrap SSH on the Worker firewall, creates the Workers with " +
		"seeded host keys, logs in to each with the seeded key pinned, rotates it, and closes " +
		"SSH with an API readback. Rehearsal only; `kluster down` clears it away.",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd, rehearseCore)
	},
}

func rehearseCore(ctx context.Context, a *app) (err error) {
	if err := a.requireRehearsal("kluster rehearse core"); err != nil {
		return err
	}
	shared, err := a.module(ctx, config.ModuleShared)
	if err != nil {
		return err
	}
	p, _, err := tf.Converge(ctx, shared, nil, sharedIntent, a.mode, a.out)
	if err != nil {
		return err
	}
	if !a.mode.Apply && len(p.Changes) > 0 {
		fmt.Fprintln(a.out, "the Workers plan once shared exists; run with --apply to go on")
		return nil
	}

	g, err := a.gate(ctx)
	if err != nil {
		return err
	}
	// The close runs whatever happens next (ADR 0009), an interrupt
	// included, so it gets a context the interrupt does not cancel.
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
		defer cancel()
		if cerr := g.Close(closeCtx); cerr != nil {
			err = errors.Join(err, cerr)
		}
	}()
	if err := g.Open(ctx, sshgate.Worker); err != nil {
		return err
	}

	workers, err := a.module(ctx, config.ModuleWorkers)
	if err != nil {
		return err
	}
	created, err := provision.CreateWorkers(ctx, workers, nil, a.pins, a.mode, a.out)
	if err != nil || len(created) == 0 {
		if err == nil && a.mode.Apply {
			fmt.Fprintln(a.out, "no Worker was created, so there is no first login to prove")
		}
		return err
	}

	hosts, closeAll, err := a.firstLogins(ctx, created)
	defer closeAll()
	if err != nil {
		return err
	}
	return stage.RunAll(ctx, []stage.Stage{stages.RotateHostKey{Pins: a.pins, Dial: a.dialer()}}, hosts,
		stage.Options{Apply: a.mode.Apply, Out: a.out})
}

// firstLogins dials each new server as root, verifying the pinned seeded
// key, and waits for it to finish booting.
func (a *app) firstLogins(ctx context.Context, servers map[string]string) ([]*stage.Host, func(), error) {
	var clients []*remote.Client
	closeAll := func() {
		for _, c := range clients {
			_ = c.Close()
		}
	}
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)

	cb, err := a.pins.Callback()
	if err != nil {
		return nil, closeAll, err
	}
	auth, err := remote.Auth(a.cfg.SSH.IdentityFile)
	if err != nil {
		return nil, closeAll, err
	}
	var hosts []*stage.Host
	for _, n := range names {
		wait, cancel := context.WithTimeout(ctx, 5*time.Minute)
		c, err := remote.DialWait(wait, servers[n], remote.Config{User: "root", Auth: auth, HostKeyCallback: cb}, 5*time.Second)
		cancel()
		if err != nil {
			return nil, closeAll, fmt.Errorf("first login to %s: %w", n, err)
		}
		fmt.Fprintf(a.out, "[%s] first login verified against the seeded host key\n", n)
		clients = append(clients, c)
		hosts = append(hosts, &stage.Host{Name: n, Addr: servers[n], Exec: c})
	}
	return hosts, closeAll, nil
}

// dialer opens root connections that accept only what cb accepts.
func (a *app) dialer() stages.Dialer {
	return func(ctx context.Context, addr string, cb ssh.HostKeyCallback) (io.Closer, error) {
		auth, err := remote.Auth(a.cfg.SSH.IdentityFile)
		if err != nil {
			return nil, err
		}
		wait, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		return remote.DialWait(wait, addr, remote.Config{User: "root", Auth: auth, HostKeyCallback: cb}, 2*time.Second)
	}
}

func (a *app) gate(ctx context.Context) (*sshgate.Gate, error) {
	shared, err := a.module(ctx, config.ModuleShared)
	if err != nil {
		return nil, err
	}
	return &sshgate.Gate{Shared: shared, API: a.api, Mode: a.mode, Out: a.out}, nil
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the commit kluster was built from",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, _ []string) {
		rev, dirty := "unknown", ""
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				switch {
				case s.Key == "vcs.revision":
					rev = s.Value
				case s.Key == "vcs.modified" && s.Value == "true":
					dirty = " (modified)"
				}
			}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "kluster %s%s\n", rev, dirty)
	},
}
