package cloud

import (
	"context"
	"fmt"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// SSHPort is the port bootstrap SSH opens.
const SSHPort = "22"

// IsSSHRule reports whether r admits inbound TCP on port 22.
func IsSSHRule(r hcloud.FirewallRule) bool {
	return r.Direction == hcloud.FirewallRuleDirectionIn &&
		r.Protocol == hcloud.FirewallRuleProtocolTCP &&
		r.Port != nil && *r.Port == SSHPort
}

// WithoutSSH returns rules minus every inbound TCP 22 rule. The Control Plane
// firewall also holds the WireGuard rule, which must survive a close.
func WithoutSSH(rules []hcloud.FirewallRule) []hcloud.FirewallRule {
	out := []hcloud.FirewallRule{}
	for _, r := range rules {
		if !IsSSHRule(r) {
			out = append(out, r)
		}
	}
	return out
}

// SSHOpen reads the named firewall from the API and reports whether it admits
// port 22.
func (c *Client) SSHOpen(ctx context.Context, name string) (bool, error) {
	fw, err := c.firewall(ctx, name)
	if err != nil {
		return false, err
	}
	for _, r := range fw.Rules {
		if IsSSHRule(r) {
			return true, nil
		}
	}
	return false, nil
}

// ClearSSH removes every port 22 rule from the named firewall through the API
// and waits until Hetzner has applied the change to every attached server.
// It is the by-hand fix from docs/runbook/workers.md, step 9.
func (c *Client) ClearSSH(ctx context.Context, name string) error {
	fw, err := c.firewall(ctx, name)
	if err != nil {
		return err
	}
	actions, _, err := c.h.Firewall.SetRules(ctx, fw, hcloud.FirewallSetRulesOpts{Rules: WithoutSSH(fw.Rules)})
	if err != nil {
		return fmt.Errorf("set_rules on %s: %w", name, err)
	}
	if err := c.h.Action.WaitFor(ctx, actions...); err != nil {
		return fmt.Errorf("waiting for set_rules on %s: %w", name, err)
	}
	return nil
}

func (c *Client) firewall(ctx context.Context, name string) (*hcloud.Firewall, error) {
	fw, _, err := c.h.Firewall.GetByName(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("reading firewall %s: %w", name, err)
	}
	if fw == nil {
		return nil, fmt.Errorf("firewall %s does not exist", name)
	}
	return fw, nil
}
