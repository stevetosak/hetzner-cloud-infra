package seed

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Git runs git in the repository and returns its stdout.
type Git func(ctx context.Context, args ...string) ([]byte, error)

// Export fetches rev from origin and writes that commit's tree to dst. It
// returns the commit. The seed builds what ArgoCD owns from it: the
// revision ArgoCD reads, not the branch the operator is on (Lesson: a
// change is measured against the branch its consumer reads).
func Export(ctx context.Context, git Git, rev, dst string) (string, error) {
	if _, err := git(ctx, "fetch", "--quiet", "origin", rev); err != nil {
		return "", fmt.Errorf("git fetch origin %s: %w", rev, err)
	}
	out, err := git(ctx, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(out))
	ar, err := git(ctx, "archive", "--format=tar", commit)
	if err != nil {
		return "", err
	}
	return commit, untar(bytes.NewReader(ar), dst)
}

// untar writes the regular files and directories of a git archive to dst.
// A symlink or a path that leaves dst is refused.
func untar(r io.Reader, dst string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(filepath.FromSlash(h.Name))
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("archive entry %q leaves the export", h.Name)
		}
		p := filepath.Join(dst, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				return err
			}
			f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		case tar.TypeXGlobalHeader:
			// git archive's commit id header.
		default:
			return fmt.Errorf("archive entry %q: type %c is not a file or a directory", h.Name, h.Typeflag)
		}
	}
}
