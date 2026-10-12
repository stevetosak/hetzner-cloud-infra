// Package bitwarden reads the password manager, the root of every secret
// kluster uses (ADR 0003, Amendments 2026-10-04 and 2026-10-06).
//
// It reads items with rbw, an unofficial Bitwarden client whose agent holds
// the unlocked keys, so one call costs milliseconds; the official `bw` CLI
// starts Node.js on every call, about 3 s. kluster runs rbw under its own
// profile, Profile, with its own agent and a short lock timeout, and never
// unlocks it: the operator does, in their own terminal.
//
// rbw cannot download attachments (doy/rbw#130), so Attachment alone runs
// `bw`, in a shell the operator unlocked with BW_SESSION.
//
// A value read here stays in memory. Nothing in this package prints one, and
// no error carries a client's standard output, where the values are.
package bitwarden

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Profile is the rbw profile kluster reads: its own config, local vault and
// agent, apart from any profile the operator uses day to day.
const Profile = "kluster"

// Runner runs a client with args and returns its standard output. Tests
// replace it.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// ErrNotFound means the vault holds no item by that name.
var ErrNotFound = errors.New("not found")

// Client reads one vault: items through rbw, attachments through bw. Each
// client is checked and synced once per Client.
type Client struct {
	rbw, bw  Runner
	warn     io.Writer
	ready    bool
	bwSynced bool
	ids      map[string][]string // item name → the ids of the items with that name
	items    map[string]*Item    // id → item
}

// New returns a Client over the real rbw and bw. Warnings, such as a failed
// sync, go to warn.
func New(warn io.Writer) *Client {
	return NewWithRunners(rbwCLI, bwCLI, warn)
}

// NewWithRunners returns a Client over the given runners, for tests.
func NewWithRunners(rbw, bw Runner, warn io.Writer) *Client {
	return &Client{rbw: rbw, bw: bw, warn: warn}
}

// The commands the operator runs, named in errors.
const (
	rbwUnlock = "RBW_PROFILE=" + Profile + " rbw unlock"
	bwUnlock  = "export BW_SESSION=\"$(bw unlock --raw)\""
)

// rbwCLI runs rbw under Profile. The rest of kluster's environment passes
// through: rbw finds its agent socket through XDG_RUNTIME_DIR.
func rbwCLI(ctx context.Context, args ...string) ([]byte, error) {
	return run(ctx, "rbw", "rbw is not installed (docs/runbook/secrets.md)",
		append(os.Environ(), "RBW_PROFILE="+Profile), args)
}

// bwCLI runs bw. BW_SESSION reaches it from kluster's own environment.
// --nointeraction makes a locked vault fail instead of prompting.
func bwCLI(ctx context.Context, args ...string) ([]byte, error) {
	return run(ctx, "bw", "the Bitwarden CLI `bw` is not installed; it is needed for attachments only",
		os.Environ(), append([]string{"--nointeraction"}, args...))
}

func run(ctx context.Context, bin, missing string, env, args []string) ([]byte, error) {
	if _, err := exec.LookPath(bin); err != nil {
		return nil, errors.New(missing)
	}
	c := exec.CommandContext(ctx, bin, args...)
	c.Env = env
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		// stderr holds the client's message ("no entry found", "Vault is
		// locked."), never a value; stdout is dropped on purpose.
		return nil, fmt.Errorf("%s %s: %w: %s", bin, args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Ready checks that the rbw agent is unlocked, without starting or
// unlocking it, then syncs once. A sync that fails leaves the local copy,
// which still decrypts offline; kluster warns and goes on.
func (c *Client) Ready(ctx context.Context) error {
	if c.ready {
		return nil
	}
	if _, err := c.rbw(ctx, "unlocked"); err != nil {
		return fmt.Errorf("Bitwarden: the rbw profile %q is locked or not set up; run `%s` (docs/runbook/secrets.md)", Profile, rbwUnlock)
	}
	if _, err := c.rbw(ctx, "sync"); err != nil {
		fmt.Fprintf(c.warn, "warning: Bitwarden sync failed, reading the local copy: %v\n", err)
	}
	c.ready = true
	return nil
}

// Item is one vault item. Its values are reached through its methods only.
type Item struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Data   data    `json:"data"`
	Fields []field `json:"fields"`
}

// data is the type-specific part of an entry; only a login's password is
// read.
type data struct {
	Password *string `json:"password"`
}

type field struct {
	Name  *string `json:"name"`
	Value *string `json:"value"`
}

// listed is one line of `rbw list --raw`, which carries no values.
type listed struct {
	ID   string  `json:"id"`
	Name *string `json:"name"`
}

// Item reads the item named exactly name. `rbw get <name>` falls back to a
// part match, so kluster lists every item's id and name once, matches the
// name exactly, and reads the item by its id. Two items with the name are
// refused, never one picked.
func (c *Client) Item(ctx context.Context, name string) (*Item, error) {
	if err := c.Ready(ctx); err != nil {
		return nil, err
	}
	if err := c.list(ctx); err != nil {
		return nil, fmt.Errorf("Bitwarden item %q: %w", name, err)
	}
	ids := c.ids[name]
	switch len(ids) {
	case 0:
		return nil, fmt.Errorf("Bitwarden item %q: %w", name, ErrNotFound)
	case 1:
	default:
		return nil, fmt.Errorf("Bitwarden has %d items named %q; keep one", len(ids), name)
	}
	if it, ok := c.items[ids[0]]; ok {
		return it, nil
	}
	out, err := c.rbw(ctx, "get", "--raw", ids[0])
	if err != nil {
		return nil, fmt.Errorf("Bitwarden item %q: %w", name, err)
	}
	var it Item
	if err := json.Unmarshal(out, &it); err != nil {
		return nil, fmt.Errorf("Bitwarden item %q: unreadable output", name)
	}
	if it.ID != ids[0] || it.Name != name {
		return nil, fmt.Errorf("Bitwarden item %q: rbw returned another item", name)
	}
	c.items[it.ID] = &it
	return &it, nil
}

// list reads every item's id and name once.
func (c *Client) list(ctx context.Context) error {
	if c.ids != nil {
		return nil
	}
	out, err := c.rbw(ctx, "list", "--raw")
	if err != nil {
		return err
	}
	var all []listed
	if err := json.Unmarshal(out, &all); err != nil {
		return errors.New("rbw list: unreadable output")
	}
	c.ids, c.items = map[string][]string{}, map[string]*Item{}
	for _, l := range all {
		if l.Name != nil {
			c.ids[*l.Name] = append(c.ids[*l.Name], l.ID)
		}
	}
	return nil
}

// Field returns the value of a field. "password" is the login password; any
// other name is a custom field. An empty value is an error: a credential is
// never blank.
func (it *Item) Field(name string) (string, error) {
	var v string
	found := false
	if name == "password" && it.Data.Password != nil {
		v, found = *it.Data.Password, true
	} else {
		for _, f := range it.Fields {
			if f.Name != nil && *f.Name == name {
				if found {
					return "", fmt.Errorf("Bitwarden item %q has two fields named %q", it.Name, name)
				}
				found = true
				if f.Value != nil {
					v = *f.Value
				}
			}
		}
	}
	if !found {
		return "", fmt.Errorf("Bitwarden item %q has no field %q", it.Name, name)
	}
	if v == "" {
		return "", fmt.Errorf("Bitwarden item %q: field %q is empty", it.Name, name)
	}
	return v, nil
}

// Attachment returns the content of the item's attachment named fileName,
// through bw: rbw cannot download one. bw syncs first, once, so it knows
// the attachment as the vault holds it now. bw refuses a name two
// attachments share.
func (c *Client) Attachment(ctx context.Context, it *Item, fileName string) ([]byte, error) {
	wrap := func(err error) error {
		return fmt.Errorf("Bitwarden item %q, attachment %q (read with bw; run `%s` in this shell): %w", it.Name, fileName, bwUnlock, err)
	}
	if !c.bwSynced {
		if _, err := c.bw(ctx, "sync"); err != nil {
			return nil, wrap(err)
		}
		c.bwSynced = true
	}
	out, err := c.bw(ctx, "get", "attachment", fileName, "--itemid", it.ID, "--raw")
	if err != nil {
		return nil, wrap(err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("Bitwarden item %q: attachment %q is empty", it.Name, fileName)
	}
	return out, nil
}
