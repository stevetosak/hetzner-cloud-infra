package hostkey

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Pins is kluster's own known_hosts file for one environment. It holds only
// keys kluster generated or read over a verified connection, so it never
// learns a key on first use. Addresses are reused across rebuilds
// (docs/runbook/workers.md, "Stale host keys"), so setting an address
// replaces whatever was pinned there before.
type Pins struct {
	path string
}

// DefaultPinsPath is ~/.config/kluster/<env>/known_hosts.
func DefaultPinsPath(env string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "kluster", env, "known_hosts"), nil
}

// OpenPins opens the pins file at path, creating it empty if needed.
func OpenPins(path string) (*Pins, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening pins %s: %w", path, err)
	}
	return &Pins{path: path}, f.Close()
}

// Callback verifies a server's host key against the pins. An address with no
// pin is refused, never learned. It reads the file at each check, not once:
// knownhosts.New alone keeps the keys of the moment it was made, and a
// callback made before a rotation then refuses the rotated key.
func (p *Pins) Callback() (ssh.HostKeyCallback, error) {
	if _, err := knownhosts.New(p.path); err != nil {
		return nil, err
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		cb, err := knownhosts.New(p.path)
		if err != nil {
			return err
		}
		return cb(hostname, remote, key)
	}, nil
}

// Set pins exactly keys for addr, dropping every earlier pin for it.
func (p *Pins) Set(addr string, keys ...ssh.PublicKey) error {
	return p.rewrite(addr, keys, true)
}

// Add pins key for addr beside the pins it already has. The rotation uses it
// so that a run that stops halfway can still reach the host with either key.
func (p *Pins) Add(addr string, key ssh.PublicKey) error {
	return p.rewrite(addr, []ssh.PublicKey{key}, false)
}

// Remove drops every pin for addr. A removed server's addresses go back to
// the pool, and a later server there must not be trusted with the old key.
func (p *Pins) Remove(addr string) error {
	return p.rewrite(addr, nil, true)
}

func (p *Pins) rewrite(addr string, keys []ssh.PublicKey, replace bool) error {
	host := knownhosts.Normalize(addr)
	old, err := os.ReadFile(p.path)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(old))
	for sc.Scan() {
		line := sc.Text()
		if replace && lineHost(line) == host {
			continue
		}
		out.WriteString(line + "\n")
	}
	if err := sc.Err(); err != nil {
		return err
	}
	for _, k := range keys {
		out.WriteString(knownhosts.Line([]string{host}, k) + "\n")
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, out.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.path)
}

// Has reports whether addr has a pin. kluster pins only servers it created,
// so a server it has no pin for is not one of its own.
func (p *Pins) Has(addr string) (bool, error) {
	data, err := os.ReadFile(p.path)
	if err != nil {
		return false, err
	}
	host := knownhosts.Normalize(addr)
	for _, line := range strings.Split(string(data), "\n") {
		if lineHost(line) == host {
			return true, nil
		}
	}
	return false, nil
}

// Reset drops every pin. `kluster down` calls it once the project it pinned
// is empty.
func (p *Pins) Reset() error {
	return os.WriteFile(p.path, nil, 0o600)
}

// lineHost returns the host field of a known_hosts line kluster wrote.
func lineHost(line string) string {
	f := strings.Fields(line)
	if len(f) == 0 || strings.HasPrefix(f[0], "#") {
		return ""
	}
	return f[0]
}

// HostPort adds the SSH port to a bare address.
func HostPort(addr string) string {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	return net.JoinHostPort(addr, "22")
}
