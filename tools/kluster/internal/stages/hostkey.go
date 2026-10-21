// Package stages holds the concrete Stages kluster runs on hosts. Each one
// carries out the runbook section its Runbook method names.
package stages

import (
	"context"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/hostkey"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/remote"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

const (
	rotatedMarker = "/etc/ssh/kluster-host-key-rotated"
	newKeyPath    = "/etc/ssh/kluster_new_host_ed25519_key"
)

// The two halves of the rotation. They run as root, which is how the first
// login onto a fresh Hetzner image goes.
var (
	generateScript = `set -e
rm -f ` + newKeyPath + ` ` + newKeyPath + `.pub
ssh-keygen -q -t ed25519 -N '' -C '' -f ` + newKeyPath + `
cat ` + newKeyPath + `.pub`

	installScript = `set -e
rm -f /etc/ssh/ssh_host_*_key /etc/ssh/ssh_host_*_key.pub
mv ` + newKeyPath + ` /etc/ssh/ssh_host_ed25519_key
mv ` + newKeyPath + `.pub /etc/ssh/ssh_host_ed25519_key.pub
chmod 600 /etc/ssh/ssh_host_ed25519_key
systemctl restart ssh
date -u +%FT%TZ > ` + rotatedMarker
)

// Dialer opens a fresh connection to addr that accepts only what cb accepts.
type Dialer func(ctx context.Context, addr string, cb ssh.HostKeyCallback) (io.Closer, error)

// RotateHostKey replaces the host key a server booted with. That key came in
// cloud-init user_data, which Hetzner keeps serving on the metadata service
// to every process on the server, so it stops being trusted at the first
// login. The new key is made on the host and its public half read back over
// the connection the seeded key verified.
type RotateHostKey struct {
	Pins *hostkey.Pins
	Dial Dialer
}

func (RotateHostKey) Name() string { return "rotate-host-key" }

func (RotateHostKey) Runbook() string {
	return "docs/runbook/host-keys.md#rotate-the-seeded-key-at-the-first-login"
}

// Probe is done once the host has the rotated key and it is the only key
// pinned at its address. A run that stopped after the install but before the
// final pin leaves the marker with the seeded key still pinned beside the new
// one: that is not done.
func (r RotateHostKey) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	rotated, err := installed(ctx, h)
	if err != nil {
		return stage.Status{}, err
	}
	if rotated == "" {
		return stage.Status{Detail: "the host still serves the seeded key"}, nil
	}
	if r.Pins != nil {
		keys, err := r.Pins.Keys(h.Addr)
		if err != nil {
			return stage.Status{}, err
		}
		if len(keys) > 1 {
			return stage.Status{Detail: fmt.Sprintf("rotated %s, but %d keys are pinned at %s: the seeded key is not dropped yet", rotated, len(keys), h.Addr)}, nil
		}
	}
	return stage.Status{Done: true, Detail: "rotated " + rotated}, nil
}

func (RotateHostKey) Preview(ctx context.Context, h *stage.Host) (string, error) {
	if h.Exec != nil {
		rotated, err := installed(ctx, h)
		if err != nil {
			return "", err
		}
		if rotated != "" {
			return "# the host serves the rotated key since " + rotated +
				"\n# dial again accepting only the key pinned last, then pin it alone\n", nil
		}
	}
	return generateScript + "\n# pin the new key beside the seeded one\n" + installScript +
		"\n# dial again accepting only the new key, then pin it alone\n", nil
}

func (r RotateHostKey) Act(ctx context.Context, h *stage.Host) error {
	rotated, err := installed(ctx, h)
	if err != nil {
		return err
	}
	if rotated != "" {
		return r.finishPin(ctx, h)
	}
	out, err := h.Exec.Run(ctx, generateScript)
	if err != nil {
		return err
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(out)))
	if err != nil {
		return fmt.Errorf("reading the new host key: %w", err)
	}
	// Both keys pass from here until the end, so a run that stops halfway
	// can still reach the host whichever key sshd serves.
	if err := r.Pins.Add(h.Addr, key); err != nil {
		return err
	}
	if _, err := h.Exec.Run(ctx, installScript); err != nil {
		return err
	}
	c, err := r.Dial(ctx, h.Addr, remote.FixedHostKey(key))
	if err != nil {
		return fmt.Errorf("the host does not serve the new key: %w", err)
	}
	_ = c.Close()
	return r.Pins.Set(h.Addr, key)
}

// finishPin ends a rotation that stopped after the install: the host serves
// the new key, and the seeded key is still pinned beside it. The install is
// not run again. The new key is the one pinned last (Add put it after the
// seeded one); the host proves it holds it on a connection that accepts that
// key alone, and only then is it pinned alone. The key is never read from
// the host: the seeded key is public on the metadata service, so a session
// it verified cannot say which key is the new one.
func (r RotateHostKey) finishPin(ctx context.Context, h *stage.Host) error {
	key, err := r.Pins.Last(h.Addr)
	if err != nil {
		return err
	}
	if key == nil {
		return fmt.Errorf("no key is pinned at %s: kluster cannot tell which key the rotation made", h.Addr)
	}
	c, err := r.Dial(ctx, h.Addr, remote.FixedHostKey(key))
	if err != nil {
		return fmt.Errorf("the host does not serve the key the rotation pinned at %s; the pins stay as they are: %w", h.Addr, err)
	}
	_ = c.Close()
	return r.Pins.Set(h.Addr, key)
}

// installed returns when the rotated key was installed, "" while the host
// still has the seeded key.
func installed(ctx context.Context, h *stage.Host) (string, error) {
	return h.Exec.Run(ctx, "cat "+rotatedMarker+" 2>/dev/null || true")
}
