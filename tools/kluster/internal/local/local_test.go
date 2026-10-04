package local

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestReadWriteWithoutSudo(t *testing.T) {
	ctx := context.Background()
	e := Exec{}
	path := filepath.Join(t.TempDir(), "sub", "wg0.conf")
	if _, err := e.ReadFile(ctx, path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: want fs.ErrNotExist, got %v", err)
	}
	if err := e.WriteFile(ctx, path, "x\n", 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := e.ReadFile(ctx, path)
	if err != nil || got != "x\n" {
		t.Fatalf("read back %q, %v", got, err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Dir(path)); st.Mode().Perm() != 0o700 {
		t.Fatalf("parent mode %v", st.Mode().Perm())
	}
}

func TestRunTrimsAndReportsFailure(t *testing.T) {
	out, err := Exec{}.Run(context.Background(), "echo hi")
	if err != nil || out != "hi" {
		t.Fatalf("got %q, %v", out, err)
	}
	if _, err := (Exec{}).Run(context.Background(), "exit 3"); err == nil {
		t.Fatal("a failing command returned no error")
	}
}

func TestQuote(t *testing.T) {
	out, err := Exec{}.Run(context.Background(), "printf %s "+Quote("it's a path"))
	if err != nil || out != "it's a path" {
		t.Fatalf("got %q, %v", out, err)
	}
}
