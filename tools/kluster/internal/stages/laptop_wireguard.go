package stages

import (
	"context"
	"errors"
	"io/fs"
	"net/netip"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/filediff"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/wgconf"
)

// LaptopWireGuard points the operator's WireGuard config at the new hub: its
// PublicKey and, if the address changed, its Endpoint. Nothing else in the
// file changes. Run on the laptop host (internal/local).
type LaptopWireGuard struct {
	Path  string
	Unit  string // restarted after the edit; empty: none
	Sudo  bool   // restart the unit through sudo
	HubIP netip.Addr
	Hub   wgconf.HubPeer
	// Seed is written first when Path does not exist: the rehearsal's
	// stand-in. Empty: a missing file is an error.
	Seed string
}

func (LaptopWireGuard) Name() string { return "laptop-wireguard" }
func (LaptopWireGuard) Runbook() string {
	return "docs/runbook/control-plane.md#the-operators-own-config"
}

// current returns the file as it is, or the seed for a missing file.
func (l LaptopWireGuard) current(ctx context.Context, h *stage.Host) (string, bool, error) {
	s, err := h.Exec.ReadFile(ctx, l.Path)
	if errors.Is(err, fs.ErrNotExist) && l.Seed != "" {
		return l.Seed, false, nil
	}
	return s, err == nil, err
}

func (l LaptopWireGuard) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	s, err := h.Exec.ReadFile(ctx, l.Path)
	if errors.Is(err, fs.ErrNotExist) && l.Seed != "" {
		return stage.Status{Detail: l.Path + " does not exist yet"}, nil
	}
	if err != nil {
		return stage.Status{}, err
	}
	got, err := wgconf.Hub(s, l.HubIP)
	if err != nil {
		return stage.Status{}, err
	}
	if got == l.Hub {
		return stage.Status{Done: true, Detail: "hub peer " + got.PublicKey + " at " + got.Endpoint}, nil
	}
	return stage.Status{Detail: "hub peer " + got.PublicKey + " at " + got.Endpoint}, nil
}

func (l LaptopWireGuard) restart() string {
	if l.Unit == "" {
		return ""
	}
	if l.Sudo {
		return "sudo systemctl restart " + l.Unit
	}
	return "systemctl restart " + l.Unit
}

func (l LaptopWireGuard) Preview(ctx context.Context, h *stage.Host) (string, error) {
	if h.Exec == nil {
		return "# set the hub peer in " + l.Path + " to the new hub key and " + l.Hub.Endpoint + "\n" + l.restart() + "\n", nil
	}
	old, exists, err := l.current(ctx, h)
	if err != nil {
		return "", err
	}
	next, err := wgconf.SetHub(old, l.HubIP, l.Hub)
	if err != nil {
		return "", err
	}
	if !exists {
		old = ""
	}
	return filediff.Unified(l.Path, old, next) + l.restart() + "\n", nil
}

func (l LaptopWireGuard) Act(ctx context.Context, h *stage.Host) error {
	old, exists, err := l.current(ctx, h)
	if err != nil {
		return err
	}
	next, err := wgconf.SetHub(old, l.HubIP, l.Hub)
	if err != nil {
		return err
	}
	if exists {
		backup := l.Path + ".bak-kluster-" + time.Now().UTC().Format("20060102T150405Z")
		if err := h.Exec.WriteFile(ctx, backup, old, 0o600); err != nil {
			return err
		}
	}
	if err := h.Exec.WriteFile(ctx, l.Path, next, 0o600); err != nil {
		return err
	}
	if r := l.restart(); r != "" {
		_, err = h.Exec.Run(ctx, r)
	}
	return err
}
