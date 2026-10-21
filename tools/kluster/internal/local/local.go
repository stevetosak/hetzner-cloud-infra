// Package local runs a Stage's commands on the operator's own machine, so
// the laptop edits after a Control Plane build go through the same Stage
// model as a host (ADR 0009).
package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Exec is the operator's machine. With Sudo, files are read and written
// through sudo, which asks for the operator's password on the terminal; a
// run with no terminal fails there and says so.
type Exec struct {
	Sudo bool
	// TmpDir holds a file between kluster and `sudo install`. It is the
	// run directory, private to the operator.
	TmpDir string

	// sudo and terminal are seams for tests; nil runs the real sudo and
	// looks for the real terminal.
	sudo     sudoFunc
	terminal func() bool
}

// sudoFunc runs sudo with args, its stdin the operator's, and returns its
// stdout, its stderr and its exit code. err is set only when sudo did not
// run at all.
type sudoFunc func(ctx context.Context, args ...string) (stdout []byte, stderr string, code int, err error)

func runSudo(ctx context.Context, args ...string) ([]byte, string, int, error) {
	c := exec.CommandContext(ctx, "sudo", args...)
	c.Stdin = os.Stdin // sudo asks on the terminal
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out, strings.TrimSpace(stderr.String()), exit.ExitCode(), nil
	}
	return out, strings.TrimSpace(stderr.String()), 0, err
}

// hasTerminal reports whether sudo has a terminal to ask for a password on:
// it reads the password from the controlling terminal, not from stdin.
func hasTerminal() bool {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func (e Exec) runSudo(ctx context.Context, args ...string) ([]byte, string, int, error) {
	if e.sudo != nil {
		return e.sudo(ctx, args...)
	}
	return runSudo(ctx, args...)
}

// sudoReady proves sudo runs before a read goes through it, so a sudo that
// cannot run is reported as that, never as a missing file. `sudo -n true`
// passes when no password is needed. Otherwise, with a terminal, `sudo -v`
// asks for the password there, as the reads always did; with none, the run
// fails and says so.
func (e Exec) sudoReady(ctx context.Context) error {
	_, stderr, code, err := e.runSudo(ctx, "-n", "true")
	if err != nil {
		return fmt.Errorf("running sudo: %w", err)
	}
	if code == 0 {
		return nil
	}
	terminal := hasTerminal
	if e.terminal != nil {
		terminal = e.terminal
	}
	if !terminal() {
		return fmt.Errorf("sudo needs a password and there is no terminal to ask on (sudo -n: %s): run kluster in a terminal", stderr)
	}
	if _, stderr, code, err = e.runSudo(ctx, "-v"); err != nil {
		return fmt.Errorf("running sudo: %w", err)
	} else if code != 0 {
		return fmt.Errorf("sudo failed (exit %d): %s", code, stderr)
	}
	return nil
}

// Run runs cmd with bash and returns its combined output, trimmed of the
// final newline.
func (e Exec) Run(ctx context.Context, cmd string) (string, error) {
	c := exec.CommandContext(ctx, "bash", "-c", cmd)
	c.Stdin = os.Stdin // sudo asks on the terminal
	out, err := c.CombinedOutput()
	s := strings.TrimRight(string(out), "\n")
	if err != nil {
		return s, fmt.Errorf("running %q: %w (output: %s)", firstLine(cmd), err, s)
	}
	return s, nil
}

// ReadFile reads path. A missing file is an error that matches
// fs.ErrNotExist. Through sudo, only a `test -e` that sudo ran and that
// failed quietly is a missing file: sudo itself also exits 1, and says why
// on stderr, when it cannot run.
func (e Exec) ReadFile(ctx context.Context, path string) (string, error) {
	if !e.Sudo {
		b, err := os.ReadFile(path)
		return string(b), err
	}
	if err := e.sudoReady(ctx); err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	_, stderr, code, err := e.runSudo(ctx, "test", "-e", path)
	switch {
	case err != nil:
		return "", fmt.Errorf("sudo test -e %s: %w", path, err)
	case code == 1 && stderr == "":
		return "", fmt.Errorf("%s: %w", path, fs.ErrNotExist)
	case code != 0:
		return "", fmt.Errorf("sudo test -e %s: exit %d: %s", path, code, stderr)
	}
	// Exactly the file, final newline included, as os.ReadFile gives it.
	b, stderr, code, err := e.runSudo(ctx, "cat", "--", path)
	switch {
	case err != nil:
		return "", fmt.Errorf("sudo cat %s: %w", path, err)
	case code != 0:
		return "", fmt.Errorf("sudo cat %s: exit %d: %s", path, code, stderr)
	}
	return string(b), nil
}

// WriteFile writes path whole, with mode. Without sudo it writes beside the
// file and renames, so a reader never sees half a file.
func (e Exec) WriteFile(ctx context.Context, path, content string, mode os.FileMode) error {
	if !e.Sudo {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		tmp := path + ".kluster-tmp"
		if err := os.WriteFile(tmp, []byte(content), mode.Perm()); err != nil {
			return err
		}
		return os.Rename(tmp, path)
	}
	if e.TmpDir == "" {
		return errors.New("sudo write without a run directory")
	}
	f, err := os.CreateTemp(e.TmpDir, "sudo-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_, err = e.Run(ctx, fmt.Sprintf("sudo install -m %s -o root -g root %s %s",
		strconv.FormatInt(int64(mode.Perm()), 8), Quote(f.Name()), Quote(path)))
	return err
}

// Quote quotes s for bash.
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}
