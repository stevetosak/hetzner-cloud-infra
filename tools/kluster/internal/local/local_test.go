package local

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

// sudoReply is what the fake sudo answers to one command line.
type sudoReply struct {
	stdout string
	stderr string
	code   int
}

// fakeSudo answers each command line from replies and records them; a line
// it has no reply for fails the test.
func fakeSudo(t *testing.T, replies map[string]sudoReply) (sudoFunc, *[]string) {
	t.Helper()
	var ran []string
	return func(_ context.Context, args ...string) ([]byte, string, int, error) {
		line := strings.Join(args, " ")
		ran = append(ran, line)
		r, ok := replies[line]
		if !ok {
			t.Errorf("unexpected sudo %s", line)
			return nil, "", 1, nil
		}
		return []byte(r.stdout), r.stderr, r.code, nil
	}, &ran
}

const passwordRequired = "sudo: a password is required"

func TestReadFileThroughSudo(t *testing.T) {
	const path = "/etc/wireguard/wg0.conf"
	ok := sudoReply{}
	for name, c := range map[string]struct {
		replies  map[string]sudoReply
		terminal bool
		want     string // the content, or a part of the error
		notExist bool
	}{
		"no password needed, file present": {
			replies: map[string]sudoReply{"-n true": ok, "test -e " + path: ok, "cat -- " + path: {stdout: "[Interface]\n"}},
			want:    "[Interface]\n",
		},
		"no password needed, file missing": {
			replies:  map[string]sudoReply{"-n true": ok, "test -e " + path: {code: 1}},
			notExist: true,
		},
		"a password and no terminal": {
			replies: map[string]sudoReply{"-n true": {stderr: passwordRequired, code: 1}},
			want:    "no terminal to ask on (sudo -n: " + passwordRequired + ")",
		},
		"a password asked on the terminal": {
			replies: map[string]sudoReply{"-n true": {stderr: passwordRequired, code: 1}, "-v": ok,
				"test -e " + path: ok, "cat -- " + path: {stdout: "x"}},
			terminal: true,
			want:     "x",
		},
		"a wrong password on the terminal": {
			replies:  map[string]sudoReply{"-n true": {stderr: passwordRequired, code: 1}, "-v": {stderr: "sudo: 3 incorrect password attempts", code: 1}},
			terminal: true,
			want:     "sudo failed (exit 1): sudo: 3 incorrect password attempts",
		},
		"sudo fails at the test itself": {
			replies: map[string]sudoReply{"-n true": ok, "test -e " + path: {stderr: passwordRequired, code: 1}},
			want:    "sudo test -e " + path + ": exit 1: " + passwordRequired,
		},
	} {
		t.Run(name, func(t *testing.T) {
			sudo, _ := fakeSudo(t, c.replies)
			e := Exec{Sudo: true, sudo: sudo, terminal: func() bool { return c.terminal }}
			got, err := e.ReadFile(context.Background(), path)
			switch {
			case c.notExist:
				if !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("want fs.ErrNotExist, got %q, %v", got, err)
				}
			case errors.Is(err, fs.ErrNotExist):
				t.Fatalf("a sudo failure reads as a missing file: %v", err)
			case err != nil:
				if !strings.Contains(err.Error(), c.want) {
					t.Fatalf("err %v, want it to say %q", err, c.want)
				}
			case got != c.want:
				t.Fatalf("read %q, want %q", got, c.want)
			}
		})
	}
}

func TestQuote(t *testing.T) {
	out, err := Exec{}.Run(context.Background(), "printf %s "+Quote("it's a path"))
	if err != nil || out != "it's a path" {
		t.Fatalf("got %q, %v", out, err)
	}
}
