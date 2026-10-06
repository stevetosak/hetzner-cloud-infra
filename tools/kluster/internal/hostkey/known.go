package hostkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// DefaultKnownHostsPath is the operator's OpenSSH known_hosts file.
func DefaultKnownHostsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh", "known_hosts"), nil
}

// FromKnownHosts returns the one ed25519 key that the known_hosts file at
// path holds for addr. `kluster pin import` uses it once, for a server
// kluster did not create; kluster itself never reads that file to verify a
// host. Hashed entries are found too.
func FromKnownHosts(path, addr string) (ssh.PublicKey, error) {
	check, err := lookup(path, addr)
	if err != nil {
		return nil, err
	}
	keys, err := wanted(check, path, addr)
	if err != nil {
		return nil, err
	}
	var ed []ssh.PublicKey
	for _, k := range keys {
		if k.Type() == ssh.KeyAlgoED25519 {
			ed = append(ed, k)
		}
	}
	switch len(ed) {
	case 1:
		// A key listed for the address can still be revoked on another line.
		if err := check(ed[0]); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return ed[0], nil
	case 0:
		return nil, fmt.Errorf("%s holds no ed25519 host key for %s (%d of other types); kluster pins only ed25519", path, addr, len(keys))
	default:
		return nil, fmt.Errorf("%s holds %d ed25519 host keys for %s; remove the wrong one first", path, len(ed), addr)
	}
}

// Keys returns the keys pinned for addr.
func (p *Pins) Keys(addr string) ([]ssh.PublicKey, error) {
	return knownKeys(p.path, addr)
}

// knownKeys returns every key a known_hosts file holds for addr.
func knownKeys(path, addr string) ([]ssh.PublicKey, error) {
	check, err := lookup(path, addr)
	if err != nil {
		return nil, err
	}
	return wanted(check, path, addr)
}

// lookup returns the known_hosts check of path for addr.
func lookup(path, addr string) (func(ssh.PublicKey) error, error) {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return nil, fmt.Errorf("%q is not an IP address: kluster pins addresses, never names", addr)
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		return nil, err
	}
	remote := &net.TCPAddr{IP: ip.AsSlice(), Port: 22}
	return func(k ssh.PublicKey) error { return cb(HostPort(addr), remote, k) }, nil
}

// wanted returns the keys check accepts. The knownhosts package has no
// lookup, only a check: a check with a key no file holds fails with the keys
// it wanted.
func wanted(check func(ssh.PublicKey) error, path, addr string) ([]ssh.PublicKey, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	probe, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		return nil, err
	}
	var ke *knownhosts.KeyError
	if err := check(probe); !errors.As(err, &ke) {
		return nil, fmt.Errorf("looking up %s in %s: unexpected result %v", addr, path, err)
	}
	keys := make([]ssh.PublicKey, 0, len(ke.Want))
	for _, w := range ke.Want {
		keys = append(keys, w.Key)
	}
	return keys, nil
}
