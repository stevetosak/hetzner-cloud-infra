package cloud

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// Inventory is every resource in a project that `kluster down` deletes.
type Inventory struct {
	Servers         []*hcloud.Server
	LoadBalancers   []*hcloud.LoadBalancer
	Volumes         []*hcloud.Volume
	PrimaryIPs      []*hcloud.PrimaryIP
	FloatingIPs     []*hcloud.FloatingIP
	Firewalls       []*hcloud.Firewall
	Networks        []*hcloud.Network
	SSHKeys         []*hcloud.SSHKey
	PlacementGroups []*hcloud.PlacementGroup
	Certificates    []*hcloud.Certificate
}

// Item is one inventory entry, for printing.
type Item struct {
	Kind    string
	Name    string
	ID      int64
	Created time.Time
}

// Items lists the inventory in deletion order.
func (inv *Inventory) Items() []Item {
	var out []Item
	add := func(kind, name string, id int64, created time.Time) {
		out = append(out, Item{kind, name, id, created})
	}
	for _, x := range inv.Servers {
		add("server", x.Name, x.ID, x.Created)
	}
	for _, x := range inv.LoadBalancers {
		add("load balancer", x.Name, x.ID, x.Created)
	}
	for _, x := range inv.Volumes {
		add("volume", x.Name, x.ID, x.Created)
	}
	for _, x := range inv.PrimaryIPs {
		add("primary IP", x.Name, x.ID, x.Created)
	}
	for _, x := range inv.FloatingIPs {
		add("floating IP", x.Name, x.ID, x.Created)
	}
	for _, x := range inv.Firewalls {
		add("firewall", x.Name, x.ID, x.Created)
	}
	for _, x := range inv.Networks {
		add("network", x.Name, x.ID, x.Created)
	}
	for _, x := range inv.SSHKeys {
		add("ssh key", x.Name, x.ID, x.Created)
	}
	for _, x := range inv.PlacementGroups {
		add("placement group", x.Name, x.ID, x.Created)
	}
	for _, x := range inv.Certificates {
		add("certificate", x.Name, x.ID, x.Created)
	}
	return out
}

// Empty reports whether the project holds nothing `down` would delete.
func (inv *Inventory) Empty() bool { return len(inv.Items()) == 0 }

// Print writes the inventory in deletion order.
func (inv *Inventory) Print(w io.Writer) {
	items := inv.Items()
	if len(items) == 0 {
		fmt.Fprintln(w, "  the project is empty")
		return
	}
	for _, it := range items {
		fmt.Fprintf(w, "  %-15s %-28s %-10d created %s\n", it.Kind, it.Name, it.ID, it.Created.UTC().Format(time.RFC3339))
	}
}

// Inventory reads every resource in the project.
func (c *Client) Inventory(ctx context.Context) (*Inventory, error) {
	var inv Inventory
	var errs []error
	collect := func(what string, err error) {
		if err != nil {
			errs = append(errs, fmt.Errorf("listing %s: %w", what, err))
		}
	}
	var err error
	inv.Servers, err = c.h.Server.All(ctx)
	collect("servers", err)
	inv.LoadBalancers, err = c.h.LoadBalancer.All(ctx)
	collect("load balancers", err)
	inv.Volumes, err = c.h.Volume.All(ctx)
	collect("volumes", err)
	inv.PrimaryIPs, err = c.h.PrimaryIP.All(ctx)
	collect("primary IPs", err)
	inv.FloatingIPs, err = c.h.FloatingIP.All(ctx)
	collect("floating IPs", err)
	inv.Firewalls, err = c.h.Firewall.All(ctx)
	collect("firewalls", err)
	inv.Networks, err = c.h.Network.All(ctx)
	collect("networks", err)
	inv.SSHKeys, err = c.h.SSHKey.All(ctx)
	collect("ssh keys", err)
	inv.PlacementGroups, err = c.h.PlacementGroup.All(ctx)
	collect("placement groups", err)
	inv.Certificates, err = c.h.Certificate.All(ctx)
	collect("certificates", err)
	return &inv, errors.Join(errs...)
}
