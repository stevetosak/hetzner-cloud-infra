package workerset

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrUncommitted is returned while the Worker set differs from the commit.
var ErrUncommitted = fmt.Errorf("the Worker set is not committed")

// CheckCommitted is the commit gate: kluster runs no Terraform against a
// Worker set that git does not hold exactly as it is on disk (ADR 0009). It
// only reads; kluster never runs a git write.
func CheckCommitted(ctx context.Context, path string) error {
	dir, file := filepath.Split(path)
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := git("ls-files", "--error-unmatch", "--", file); err != nil {
		return fmt.Errorf("%w: git does not track %s; commit it, then run again", ErrUncommitted, path)
	}
	out, err := git("status", "--porcelain", "--", file)
	if err != nil {
		return fmt.Errorf("git status %s: %v: %s", path, err, out)
	}
	if out != "" {
		return fmt.Errorf("%w: %s has changes git does not hold (%s); commit them, then run again", ErrUncommitted, path, out)
	}
	return nil
}
