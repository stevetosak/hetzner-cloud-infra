// Package local runs a Stage's commands on the operator's own machine, so
// the laptop edits after a Control Plane build go through the same Stage
// model as a host (ADR 0009).
package local

import (
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
// fs.ErrNotExist.
func (e Exec) ReadFile(ctx context.Context, path string) (string, error) {
	if !e.Sudo {
		b, err := os.ReadFile(path)
		return string(b), err
	}
	if _, err := e.Run(ctx, "sudo test -e "+Quote(path)); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "", fmt.Errorf("%s: %w", path, fs.ErrNotExist)
		}
		return "", err
	}
	// Exactly the file, final newline included, as os.ReadFile gives it.
	c := exec.CommandContext(ctx, "sudo", "cat", "--", path)
	c.Stdin = os.Stdin
	b, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("sudo cat %s: %w", path, err)
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
