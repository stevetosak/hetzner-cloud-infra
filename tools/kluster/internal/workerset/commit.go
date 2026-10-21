package workerset

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrUncommitted is returned while the Worker set differs from the commit.
var ErrUncommitted = fmt.Errorf("the Worker set is not committed")

// CheckCommitted is the commit gate: kluster runs no Terraform against a
// Worker set that git does not hold exactly as it is on disk (ADR 0009). It
// only reads; kluster never runs a git write. A set that passes but whose
// last commit the upstream branch does not hold gets a warning on out, not a
// refusal: the commit exists, it is only not shared yet.
func CheckCommitted(ctx context.Context, path string, out io.Writer) error {
	dir, file := filepath.Split(path)
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		b, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(b)), err
	}
	if _, err := git("ls-files", "--error-unmatch", "--", file); err != nil {
		return fmt.Errorf("%w: git does not track %s; commit it, then run again", ErrUncommitted, path)
	}
	status, err := git("status", "--porcelain", "--", file)
	if err != nil {
		return fmt.Errorf("git status %s: %v: %s", path, err, status)
	}
	if status != "" {
		return fmt.Errorf("%w: %s has changes git does not hold (%s); commit them, then run again", ErrUncommitted, path, status)
	}
	if w := upstreamWarning(git, path, file); w != "" {
		fmt.Fprintln(out, w)
	}
	return nil
}

// upstreamWarning is "" when the upstream branch holds the last commit that
// changed the Worker set, as the last fetch knows it, and a warning
// otherwise. A git failure here is a warning too: it must not stop a run the
// commit gate passed.
func upstreamWarning(git func(args ...string) (string, error), path, file string) string {
	commit, err := git("log", "-1", "--format=%H", "--", file)
	if err != nil || commit == "" {
		return fmt.Sprintf("WARNING: could not read the last commit of %s (%v: %s); not checked that the upstream branch holds it", path, err, commit)
	}
	short := commit[:min(len(commit), 12)]
	if up, err := git("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err != nil {
		return fmt.Sprintf("WARNING: the checked-out branch has no upstream (%s), so no shared branch is known to hold %s's commit %s: push it", up, path, short)
	}
	out, err := git("merge-base", "--is-ancestor", commit, "@{upstream}")
	var exit *exec.ExitError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return fmt.Sprintf("WARNING: %s was last changed in commit %s, which the upstream branch does not hold (as of the last fetch): push it, so the Worker set kluster acts on is the one the repository shares", path, short)
	default:
		return fmt.Sprintf("WARNING: could not check that the upstream branch holds %s's commit %s: %v: %s", path, short, err, out)
	}
}
