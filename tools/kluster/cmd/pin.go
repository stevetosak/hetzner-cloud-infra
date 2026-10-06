package cmd

import (
	"bytes"
	"fmt"
	"io"
	"net/netip"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/hostkey"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/tf"
)

func init() {
	pinCmd.AddCommand(pinImportCmd)
	rootCmd.AddCommand(pinCmd)
}

var pinCmd = &cobra.Command{
	Use:   "pin",
	Short: "kluster's own host key pins (~/.config/kluster/<env>/known_hosts)",
}

var pinImportCmd = &cobra.Command{
	Use:   "import <vpn-address>",
	Short: "Pin the host key of a server kluster did not build, from ~/.ssh/known_hosts, once",
	Long: "kluster verifies every host only against its own pins. A server built by hand, such as the " +
		"live Control Plane, has none, so this copies the ed25519 key the operator's known_hosts holds " +
		"for its VPN address. Plan Mode prints the key and its SHA256 fingerprint; compare it on the host " +
		"with `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` before --apply. Only VPN addresses are " +
		"taken: a pinned public address means a server kluster created (cp init).",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(flags.config)
		if err != nil {
			return err
		}
		if _, err := cfg.Env(flags.env); err != nil {
			return err
		}
		pinsPath, err := hostkey.DefaultPinsPath(flags.env)
		if err != nil {
			return err
		}
		knownHosts, err := hostkey.DefaultKnownHostsPath()
		if err != nil {
			return err
		}
		return pinImport(pinImportRun{
			Addr:       args[0],
			Subnet:     cfg.WireGuard.Subnet,
			KnownHosts: knownHosts,
			PinsPath:   pinsPath,
			Env:        flags.env,
			Apply:      flags.apply,
		}, cmd.OutOrStdout())
	},
}

// pinImportRun is one `kluster pin import`, with every path given, so a test
// can run it against files of its own.
type pinImportRun struct {
	Addr, Subnet         string
	KnownHosts, PinsPath string
	Env                  string
	Apply                bool
}

func pinImport(r pinImportRun, out io.Writer) error {
	if err := requireInSubnet(r.Addr, r.Subnet); err != nil {
		return err
	}
	pins, err := hostkey.OpenPins(r.PinsPath)
	if err != nil {
		return err
	}
	key, err := hostkey.FromKnownHosts(r.KnownHosts, r.Addr)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "kluster: env %s, %s\n", r.Env, modeName(tf.Mode{Apply: r.Apply}))
	fmt.Fprintf(out, "%s holds for %s:\n  %s\n  %s\n", r.KnownHosts, r.Addr, hostkey.AuthorizedKey(key), ssh256(key))

	current, err := pins.Keys(r.Addr)
	if err != nil {
		return err
	}
	if len(current) == 1 && bytes.Equal(current[0].Marshal(), key.Marshal()) {
		fmt.Fprintf(out, "already pinned in %s: nothing to do\n", r.PinsPath)
		return nil
	}
	for _, k := range current {
		fmt.Fprintf(out, "replaces the pin %s\n", ssh256(k))
	}
	if !r.Apply {
		fmt.Fprintf(out, "Plan Mode: compare the fingerprint on the host (ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub); --apply pins it in %s\n", r.PinsPath)
		return nil
	}
	if err := pins.Set(r.Addr, key); err != nil {
		return err
	}
	after, err := pins.Keys(r.Addr)
	if err != nil {
		return err
	}
	if len(after) != 1 || !bytes.Equal(after[0].Marshal(), key.Marshal()) {
		return fmt.Errorf("read back: %s holds %d keys for %s, want exactly the imported one", r.PinsPath, len(after), r.Addr)
	}
	fmt.Fprintf(out, "pinned %s at %s; read back from %s\n", ssh256(key), r.Addr, r.PinsPath)
	return nil
}

// requireInSubnet refuses an address outside the WireGuard subnet.
func requireInSubnet(addr, subnet string) error {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return fmt.Errorf("%q is not an IP address", addr)
	}
	p, err := netip.ParsePrefix(subnet)
	if err != nil {
		return fmt.Errorf("wireguard.subnet: %w", err)
	}
	if !p.Contains(ip) {
		return fmt.Errorf("%s is outside the VPN subnet %s: kluster imports only VPN addresses, because a pinned public address marks a server kluster created", addr, subnet)
	}
	return nil
}

// ssh256 is a key's type and SHA256 fingerprint, as ssh-keygen -l prints it.
func ssh256(k ssh.PublicKey) string { return k.Type() + " " + ssh.FingerprintSHA256(k) }
