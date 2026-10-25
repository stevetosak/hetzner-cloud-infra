// Package cloud reads and writes the Hetzner project over its API, for the
// checks Terraform cannot be trusted with: the firewall read back after a
// close (ADR 0009, provider 1.69.0), the rehearsal guard and `kluster down`.
package cloud

import (
	"context"
	"fmt"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// Client is a Hetzner API client for one project.
type Client struct {
	h *hcloud.Client
	// unassign bounds how long DeleteAll waits for Hetzner to unassign a
	// Primary IP from a server it has just deleted.
	unassign retry
}

type retry struct {
	every, upTo time.Duration
}

// New returns a client for the project the token belongs to.
func New(token string, opts ...hcloud.ClientOption) *Client {
	opts = append([]hcloud.ClientOption{
		hcloud.WithToken(token),
		hcloud.WithApplication("kluster", "0"),
	}, opts...)
	return &Client{h: hcloud.NewClient(opts...), unassign: retry{every: 2 * time.Second, upTo: 2 * time.Minute}}
}

// GuardNotProject refuses a project that holds ip on any server or primary
// IP. A rehearsal run calls it with the live Control Plane's address before
// it does anything, so a live token under a rehearsal name stops there.
func (c *Client) GuardNotProject(ctx context.Context, ip string) error {
	ips, err := c.h.PrimaryIP.All(ctx)
	if err != nil {
		return fmt.Errorf("listing primary IPs: %w", err)
	}
	for _, p := range ips {
		if p.IP != nil && p.IP.String() == ip {
			return fmt.Errorf("this project holds primary IP %s (%s): it is the live project, refusing", ip, p.Name)
		}
	}
	servers, err := c.h.Server.All(ctx)
	if err != nil {
		return fmt.Errorf("listing servers: %w", err)
	}
	for _, s := range servers {
		if s.PublicNet.IPv4.IP != nil && s.PublicNet.IPv4.IP.String() == ip {
			return fmt.Errorf("this project holds server %s at %s: it is the live project, refusing", s.Name, ip)
		}
	}
	return nil
}
