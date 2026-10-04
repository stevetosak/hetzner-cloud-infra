package cloud

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// DeleteAll deletes every resource in inv, lifting delete and rebuild
// protection first. Only `kluster down` calls it, only for the rehearsal
// project, and only after GuardNotProject passed. It goes through the API and
// not `terraform destroy`, because the shared Module's prevent_destroy guards
// refuse a destroy by design.
//
// Order matters: servers and load balancers go first, so volumes, primary
// IPs, firewalls and the network are no longer attached when their turn
// comes.
func (c *Client) DeleteAll(ctx context.Context, inv *Inventory, log io.Writer) error {
	off := hcloud.Ptr(false)
	wait := c.waiter(ctx)
	var errs []error
	step := func(what string, err error) {
		// A server's own primary IPs (auto_delete) go with the server, so
		// some items in inv are already gone when their turn comes.
		if hcloud.IsError(err, hcloud.ErrorCodeNotFound) {
			fmt.Fprintf(log, "  already gone: %s\n", what)
			return
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", what, err))
			return
		}
		fmt.Fprintf(log, "  deleted %s\n", what)
	}

	for _, s := range inv.Servers {
		step("server "+s.Name, c.deleteServer(ctx, s))
	}
	for _, lb := range inv.LoadBalancers {
		if lb.Protection.Delete {
			if err := wait(c.h.LoadBalancer.ChangeProtection(ctx, lb, hcloud.LoadBalancerChangeProtectionOpts{Delete: off})); err != nil {
				step("load balancer "+lb.Name, err)
				continue
			}
		}
		_, err := c.h.LoadBalancer.Delete(ctx, lb)
		step("load balancer "+lb.Name, err)
	}
	// A failed server or load balancer delete leaves the rest attached, so
	// stop here rather than pile up "in use" errors.
	if err := errors.Join(errs...); err != nil {
		return err
	}

	for _, v := range inv.Volumes {
		if v.Protection.Delete {
			if err := wait(c.h.Volume.ChangeProtection(ctx, v, hcloud.VolumeChangeProtectionOpts{Delete: off})); err != nil {
				step("volume "+v.Name, err)
				continue
			}
		}
		_, err := c.h.Volume.Delete(ctx, v)
		step("volume "+v.Name, err)
	}
	for _, ip := range inv.PrimaryIPs {
		if ip.Protection.Delete {
			if err := wait(c.h.PrimaryIP.ChangeProtection(ctx, hcloud.PrimaryIPChangeProtectionOpts{ID: ip.ID, Delete: false})); err != nil {
				step("primary IP "+ip.Name, err)
				continue
			}
		}
		_, err := c.h.PrimaryIP.Delete(ctx, ip)
		step("primary IP "+ip.Name, err)
	}
	for _, ip := range inv.FloatingIPs {
		if ip.Protection.Delete {
			if err := wait(c.h.FloatingIP.ChangeProtection(ctx, ip, hcloud.FloatingIPChangeProtectionOpts{Delete: off})); err != nil {
				step("floating IP "+ip.Name, err)
				continue
			}
		}
		_, err := c.h.FloatingIP.Delete(ctx, ip)
		step("floating IP "+ip.Name, err)
	}
	for _, fw := range inv.Firewalls {
		_, err := c.h.Firewall.Delete(ctx, fw)
		step("firewall "+fw.Name, err)
	}
	for _, n := range inv.Networks {
		if n.Protection.Delete {
			if err := wait(c.h.Network.ChangeProtection(ctx, n, hcloud.NetworkChangeProtectionOpts{Delete: off})); err != nil {
				step("network "+n.Name, err)
				continue
			}
		}
		_, err := c.h.Network.Delete(ctx, n)
		step("network "+n.Name, err)
	}
	for _, k := range inv.SSHKeys {
		_, err := c.h.SSHKey.Delete(ctx, k)
		step("ssh key "+k.Name, err)
	}
	for _, pg := range inv.PlacementGroups {
		_, err := c.h.PlacementGroup.Delete(ctx, pg)
		step("placement group "+pg.Name, err)
	}
	for _, cert := range inv.Certificates {
		_, err := c.h.Certificate.Delete(ctx, cert)
		step("certificate "+cert.Name, err)
	}
	return errors.Join(errs...)
}

func (c *Client) deleteServer(ctx context.Context, s *hcloud.Server) error {
	if s.Protection.Delete || s.Protection.Rebuild {
		off := hcloud.Ptr(false)
		wait := c.waiter(ctx)
		if err := wait(c.h.Server.ChangeProtection(ctx, s, hcloud.ServerChangeProtectionOpts{Delete: off, Rebuild: off})); err != nil {
			return fmt.Errorf("lifting protection: %w", err)
		}
	}
	res, _, err := c.h.Server.DeleteWithResult(ctx, s)
	if err != nil {
		return err
	}
	return c.h.Action.WaitFor(ctx, res.Action)
}

// waiter returns a function that takes an API call's results directly and
// waits for its action.
func (c *Client) waiter(ctx context.Context) func(*hcloud.Action, *hcloud.Response, error) error {
	return func(a *hcloud.Action, _ *hcloud.Response, err error) error {
		if err != nil {
			return err
		}
		return c.h.Action.WaitFor(ctx, a)
	}
}
