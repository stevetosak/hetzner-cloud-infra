// Package routeproof proves the operator route through a new hub before the
// public SSH route is withdrawn (docs/runbook/control-plane.md, step 3). It
// adds kluster's own short-lived peer to the hub, logs in as the operator
// user over the tunnel, asks the API server for /readyz with the laptop's
// kubeconfig over the tunnel, and removes the peer again.
package routeproof

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kubeconfig"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/remote"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/vpn"
)

// Runbook is the section the proof carries out.
const Runbook = "docs/runbook/control-plane.md#verified-from-both-ends"

// Proof is one check of the route.
type Proof struct {
	Hub        *stage.Host // a root session on the hub over the public route
	HubKey     vpn.Key     // the hub's WireGuard public key
	Endpoint   netip.AddrPort
	HubIP      netip.Addr // the hub's VPN address
	ProbeIP    netip.Addr // kluster's own peer address
	User       string     // the operator user, cp-dev
	Hostname   string     // what the hub must call itself
	Auth       ssh.AuthMethod
	HostKeys   ssh.HostKeyCallback // must accept the hub at HubIP
	Kubeconfig []byte              // the laptop's, renamed
	Out        io.Writer
}

// Run proves the route. The peer is removed whatever happens; it was added
// with `wg set`, so it lives only in the running interface and a restart of
// wg0 drops it too.
func (p Proof) Run(ctx context.Context) (err error) {
	priv, err := vpn.GenerateKey()
	if err != nil {
		return err
	}
	pub := priv.Public()
	if _, err := p.Hub.Exec.Run(ctx, fmt.Sprintf("wg set wg0 peer %s allowed-ips %s/32", pub, p.ProbeIP)); err != nil {
		return fmt.Errorf("adding kluster's peer to the hub: %w", err)
	}
	defer func() {
		rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if _, rerr := p.Hub.Exec.Run(rmCtx, "wg set wg0 peer "+pub.String()+" remove"); rerr != nil {
			err = errors.Join(err, fmt.Errorf("removing kluster's peer from the hub: %w", rerr))
		}
	}()

	tun, err := vpn.Open(priv, p.ProbeIP, vpn.Peer{
		PublicKey:  p.HubKey,
		Endpoint:   p.Endpoint,
		AllowedIPs: []netip.Prefix{netip.PrefixFrom(p.HubIP, 32)},
	})
	if err != nil {
		return err
	}
	defer tun.Close()

	dialCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	c, err := remote.DialVia(dialCtx, tun.DialContext, p.HubIP.String(), remote.Config{User: p.User, Auth: p.Auth, HostKeyCallback: p.HostKeys})
	if err != nil {
		return fmt.Errorf("SSH to %s@%s through the tunnel: %w", p.User, p.HubIP, err)
	}
	defer c.Close()
	out, err := c.Run(ctx, `whoami; hostname; echo "$SSH_CONNECTION"; sudo -n id -un`)
	if err != nil {
		return err
	}
	if err := p.check(out); err != nil {
		return err
	}
	fmt.Fprintf(p.Out, "read back: SSH as %s over the tunnel; the hub saw the client as %s; sudo works\n", p.User, p.ProbeIP)

	if err := kubeconfig.Readyz(ctx, p.Kubeconfig, tun.DialContext); err != nil {
		return fmt.Errorf("the laptop kubeconfig over the tunnel: %w", err)
	}
	fmt.Fprintf(p.Out, "read back: https://%s:6443/readyz answers ok to the laptop kubeconfig over the tunnel\n", p.HubIP)
	return nil
}

// check reads the four lines the login printed: the user, the hostname, the
// connection as the hub saw it, and the user sudo gives.
func (p Proof) check(out string) error {
	l := strings.Split(strings.TrimSpace(out), "\n")
	if len(l) != 4 {
		return fmt.Errorf("unexpected login output: %q", out)
	}
	var errs []error
	if l[0] != p.User {
		errs = append(errs, fmt.Errorf("logged in as %q, want %q", l[0], p.User))
	}
	if l[1] != p.Hostname {
		errs = append(errs, fmt.Errorf("the hub calls itself %q, want %q", l[1], p.Hostname))
	}
	if f := strings.Fields(l[2]); len(f) == 0 || f[0] != p.ProbeIP.String() {
		errs = append(errs, fmt.Errorf("the hub saw the client as %q, want %s: the session did not ride the tunnel", l[2], p.ProbeIP))
	}
	if l[3] != "root" {
		errs = append(errs, fmt.Errorf("sudo gives %q, want root", l[3]))
	}
	return errors.Join(errs...)
}
