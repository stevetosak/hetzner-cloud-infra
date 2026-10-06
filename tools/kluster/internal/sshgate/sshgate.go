// Package sshgate opens and closes bootstrap SSH on the cluster's firewalls
// and proves the close over the Hetzner API (ADR 0004, ADR 0009).
//
// The hcloud provider 1.69.0 reports success when it takes a firewall from
// one rule to none and leaves the rule in place. Both runbooks met it
// (docs/runbook/workers.md, step 9). So a close is never trusted: it is read
// back, cleared through the API if the rule survived, read back again, and
// followed by a plan that must be clean.
package sshgate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/intent"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/tf"
)

// Runbook is the section every close carries out.
const Runbook = "docs/runbook/workers.md#9-close-bootstrap-ssh"

// ErrStillOpen is returned while port 22 is still open after a close.
var ErrStillOpen = errors.New("bootstrap SSH is still open")

// Firewall is one bootstrap SSH switch in the shared Module.
type Firewall struct {
	Var     string // the shared Module's variable
	Name    string // the Hetzner firewall name
	Address string // the Terraform resource address
}

var (
	ControlPlane = Firewall{"allow_public_ssh_cp", "tosak-cp-firewall", "hcloud_firewall.cp"}
	Worker       = Firewall{"allow_public_ssh_worker", "tosak-worker-firewall", "hcloud_firewall.worker"}
	All          = []Firewall{ControlPlane, Worker}
)

// API is the part of the Hetzner API a Gate reads and writes.
type API interface {
	SSHOpen(ctx context.Context, firewall string) (bool, error)
	ClearSSH(ctx context.Context, firewall string) error
}

// Planner is the part of a Terraform Module a Gate drives.
type Planner interface {
	Plan(ctx context.Context, vars map[string]any) (*tf.Plan, error)
	Apply(ctx context.Context, p *tf.Plan) error
}

// Gate opens and closes bootstrap SSH through the shared Module.
type Gate struct {
	Shared Planner
	API    API
	Mode   tf.Mode
	Out    io.Writer
}

// Open opens port 22 on each of fws to the operator's address, in one plan.
// Its Intent is an update of those firewalls and nothing else; a firewall
// not named stays as it is, closed. One plan for all of them, because the
// shared Module sets both switches: an open of one alone would close the
// other.
func (g *Gate) Open(ctx context.Context, fws ...Firewall) error {
	vars := map[string]any{}
	names := make([]string, 0, len(fws))
	in := intent.Intent{}
	for _, fw := range fws {
		vars[fw.Var] = true
		names = append(names, fw.Name)
		in.Expectations = append(in.Expectations,
			intent.Expectation{Address: fw.Address, Actions: []intent.Action{intent.Update}})
	}
	in.Description = "open bootstrap SSH on " + strings.Join(names, " and ")
	p, err := g.Shared.Plan(ctx, vars)
	if err != nil {
		return err
	}
	// No change: already open — a run that stopped before its close, or
	// `ssh open`. Nothing to apply, and the API must agree.
	did := "plans no change"
	if len(p.Changes) > 0 {
		applied, err := tf.Decide(ctx, g.Shared, p, in, g.Mode, g.Out)
		if err != nil || !applied {
			return err
		}
		did = "applied the open"
	}
	// Zero rules to one is the direction the provider performs correctly,
	// and it is still read back.
	for _, fw := range fws {
		open, err := g.API.SSHOpen(ctx, fw.Name)
		if err != nil {
			return err
		}
		if !open {
			return fmt.Errorf("%s: terraform %s, but the API shows no port 22 rule", fw.Name, did)
		}
		fmt.Fprintf(g.Out, "read back: %s admits port 22\n", fw.Name)
	}
	return nil
}

// Close closes port 22 on both firewalls and proves it. It returns
// ErrStillOpen while any firewall still admits port 22.
func (g *Gate) Close(ctx context.Context) error {
	fmt.Fprintf(g.Out, "close bootstrap SSH (%s)\n", Runbook)
	in := intent.Intent{Description: "close bootstrap SSH on both firewalls"}
	for _, fw := range All {
		in.Expectations = append(in.Expectations, intent.Expectation{
			Address: fw.Address, Actions: []intent.Action{intent.Update},
		})
	}
	if _, err := g.converge(ctx, map[string]any{}, in); err != nil {
		return err
	}

	var still []string
	for _, fw := range All {
		open, err := g.API.SSHOpen(ctx, fw.Name)
		if err != nil {
			return err
		}
		if !open {
			fmt.Fprintf(g.Out, "read back: %s admits no port 22\n", fw.Name)
			continue
		}
		if !g.Mode.Apply {
			fmt.Fprintf(g.Out, "read back: %s admits port 22; --apply closes it\n", fw.Name)
			still = append(still, fw.Name)
			continue
		}
		fmt.Fprintf(g.Out, "read back: %s still admits port 22 after the apply (provider 1.69.0); clearing it over the API\n", fw.Name)
		if err := g.API.ClearSSH(ctx, fw.Name); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrStillOpen, fw.Name, err)
		}
		if open, err := g.API.SSHOpen(ctx, fw.Name); err != nil {
			return err
		} else if open {
			still = append(still, fw.Name)
			continue
		}
		fmt.Fprintf(g.Out, "read back: %s admits no port 22\n", fw.Name)
	}
	if len(still) > 0 {
		return fmt.Errorf("%w on %v", ErrStillOpen, still)
	}
	if !g.Mode.Apply {
		return nil
	}

	// State and API must now agree: Terraform has nothing left to do.
	p, err := g.Shared.Plan(ctx, map[string]any{})
	if err != nil {
		return err
	}
	if len(p.Changes) > 0 {
		return fmt.Errorf("the shared Module is not clean after the close:\n%s", intent.Summary(p.Changes))
	}
	fmt.Fprintln(g.Out, "plan shared: no changes")
	return nil
}

func (g *Gate) converge(ctx context.Context, vars map[string]any, in intent.Intent) (bool, error) {
	p, err := g.Shared.Plan(ctx, vars)
	if err != nil {
		return false, err
	}
	return tf.Decide(ctx, g.Shared, p, in, g.Mode, g.Out)
}
