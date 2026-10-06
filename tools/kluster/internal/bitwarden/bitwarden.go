// Package bitwarden reads the password manager, the root of every secret
// kluster uses (ADR 0003, Amendments 2026-10-04 and 2026-10-06). It runs the
// Bitwarden CLI, `bw`, in an already unlocked shell: kluster never asks for
// the master password and never logs in.
//
// A value read here stays in memory. Nothing in this package prints one, and
// no error carries the CLI's standard output, where the values are.
package bitwarden

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Runner runs `bw` with args and returns its standard output. Tests replace
// it; the CLI is the only real one.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// ErrNotFound means the vault holds no item by that name.
var ErrNotFound = errors.New("not found")

// Client reads one unlocked vault.
type Client struct {
	run    Runner
	warn   io.Writer
	synced bool
}

// New returns a Client that runs the `bw` binary. Warnings, such as a failed
// sync, go to warn.
func New(warn io.Writer) *Client { return &Client{run: cli, warn: warn} }

// NewWithRunner returns a Client over run, for tests.
func NewWithRunner(run Runner, warn io.Writer) *Client { return &Client{run: run, warn: warn} }

// cli runs the real binary. BW_SESSION reaches it from kluster's own
// environment. --nointeraction makes a locked vault fail instead of
// prompting for the master password.
func cli(ctx context.Context, args ...string) ([]byte, error) {
	if _, err := exec.LookPath("bw"); err != nil {
		return nil, errors.New("the Bitwarden CLI `bw` is not installed (docs/runbook/secrets.md)")
	}
	c := exec.CommandContext(ctx, "bw", append([]string{"--nointeraction"}, args...)...)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		// stderr holds the CLI's message ("Not found.", "Vault is locked."),
		// never a value; stdout is dropped on purpose.
		return nil, fmt.Errorf("bw %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// unlockHint is what the operator runs once per shell.
const unlockHint = "run: export BW_SESSION=\"$(bw unlock --raw)\""

// Ready checks the vault is unlocked, then syncs it once per Client. A sync
// that fails leaves the local cache, which still decrypts offline; kluster
// warns and goes on.
func (c *Client) Ready(ctx context.Context) error {
	out, err := c.run(ctx, "status")
	if err != nil {
		return err
	}
	var st struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return fmt.Errorf("bw status: unreadable output: %w", err)
	}
	switch st.Status {
	case "unlocked":
	case "unauthenticated":
		return errors.New("Bitwarden: not logged in; run `bw login`, then " + unlockHint)
	default:
		return fmt.Errorf("Bitwarden: the vault is %s; %s", st.Status, unlockHint)
	}
	if c.synced {
		return nil
	}
	if _, err := c.run(ctx, "sync"); err != nil {
		fmt.Fprintf(c.warn, "warning: Bitwarden sync failed, reading the local cache: %v\n", err)
	}
	c.synced = true
	return nil
}

// Item is one vault item. Its values are reached through its methods only.
type Item struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Login       *login       `json:"login"`
	Fields      []field      `json:"fields"`
	Attachments []attachment `json:"attachments"`
}

type login struct {
	Password string `json:"password"`
}

type field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type attachment struct {
	ID       string `json:"id"`
	FileName string `json:"fileName"`
}

// Item reads the item named exactly name. `bw get item` searches, so it can
// return an item whose name only contains the term; that is refused. Two
// items with the name make the CLI fail, and kluster with it: a duplicate
// never picks one.
func (c *Client) Item(ctx context.Context, name string) (*Item, error) {
	if err := c.Ready(ctx); err != nil {
		return nil, err
	}
	out, err := c.run(ctx, "get", "item", name)
	if err != nil {
		if strings.Contains(err.Error(), "Not found") {
			return nil, fmt.Errorf("Bitwarden item %q: %w", name, ErrNotFound)
		}
		return nil, fmt.Errorf("Bitwarden item %q: %w", name, err)
	}
	var it Item
	if err := json.Unmarshal(out, &it); err != nil {
		return nil, fmt.Errorf("Bitwarden item %q: unreadable output", name)
	}
	if it.Name != name {
		return nil, fmt.Errorf("Bitwarden item %q: the search found %q, not an exact match", name, it.Name)
	}
	return &it, nil
}

// Field returns the value of a field. "password" is the login password; any
// other name is a custom field. An empty value is an error: a credential is
// never blank.
func (it *Item) Field(name string) (string, error) {
	var v string
	found := false
	if name == "password" && it.Login != nil {
		v, found = it.Login.Password, true
	} else {
		for _, f := range it.Fields {
			if f.Name == name {
				if found {
					return "", fmt.Errorf("Bitwarden item %q has two fields named %q", it.Name, name)
				}
				v, found = f.Value, true
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

// Attachment returns the content of the attachment named fileName.
func (c *Client) Attachment(ctx context.Context, it *Item, fileName string) ([]byte, error) {
	var id string
	for _, a := range it.Attachments {
		if a.FileName == fileName {
			if id != "" {
				return nil, fmt.Errorf("Bitwarden item %q has two attachments named %q", it.Name, fileName)
			}
			id = a.ID
		}
	}
	if id == "" {
		return nil, fmt.Errorf("Bitwarden item %q has no attachment %q", it.Name, fileName)
	}
	out, err := c.run(ctx, "get", "attachment", id, "--itemid", it.ID, "--raw")
	if err != nil {
		return nil, fmt.Errorf("Bitwarden item %q, attachment %q: %w", it.Name, fileName, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("Bitwarden item %q: attachment %q is empty", it.Name, fileName)
	}
	return out, nil
}
