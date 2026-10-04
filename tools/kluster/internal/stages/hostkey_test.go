package stages

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/hostkey"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// fakeHost answers the rotation's commands the way a host would.
type fakeHost struct {
	newKey   *hostkey.KeyPair
	serving  ssh.PublicKey
	rotated  string
	ran      []string
	failNext string // a command prefix that fails
}

func (f *fakeHost) Run(_ context.Context, cmd string) (string, error) {
	f.ran = append(f.ran, cmd)
	if f.failNext != "" && strings.HasPrefix(cmd, f.failNext) {
		return "", errors.New("command failed")
	}
	switch {
	case cmd == generateScript:
		return hostkey.AuthorizedKey(f.newKey.Public), nil
	case cmd == installScript:
		f.serving = f.newKey.Public
		f.rotated = "2026-10-04T12:00:00Z"
		return "", nil
	case strings.HasPrefix(cmd, "cat "+rotatedMarker):
		return f.rotated, nil
	}
	return "", errors.New("unexpected command " + cmd)
}

func (f *fakeHost) WriteFile(context.Context, string, string, os.FileMode) error { return nil }
func (f *fakeHost) ReadFile(context.Context, string) (string, error)             { return "", nil }

type closer struct{}

func (closer) Close() error { return nil }

func (f *fakeHost) dial(_ context.Context, addr string, cb ssh.HostKeyCallback) (io.Closer, error) {
	if err := cb(addr+":22", &net.TCPAddr{}, f.serving); err != nil {
		return nil, err
	}
	return closer{}, nil
}

func setup(t *testing.T) (*fakeHost, *hostkey.Pins, *hostkey.KeyPair, *stage.Host, RotateHostKey) {
	t.Helper()
	seeded, _ := hostkey.Generate()
	fresh, _ := hostkey.Generate()
	pins, err := hostkey.OpenPins(filepath.Join(t.TempDir(), "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	const addr = "203.0.113.9"
	if err := pins.Set(addr, seeded.Public); err != nil {
		t.Fatal(err)
	}
	fh := &fakeHost{newKey: fresh, serving: seeded.Public}
	h := &stage.Host{Name: "k8swk1", Addr: addr, Exec: fh}
	return fh, pins, seeded, h, RotateHostKey{Pins: pins, Dial: fh.dial}
}

func accepts(t *testing.T, pins *hostkey.Pins, addr string, k ssh.PublicKey) bool {
	t.Helper()
	cb, err := pins.Callback()
	if err != nil {
		t.Fatal(err)
	}
	return cb(hostkey.HostPort(addr), &net.TCPAddr{IP: net.ParseIP(addr), Port: 22}, k) == nil
}

func TestRotationLeavesOnlyTheNewKeyPinned(t *testing.T) {
	fh, pins, seeded, h, r := setup(t)
	err := stage.Run(context.Background(), []stage.Stage{r}, h, stage.Options{Apply: true, Out: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if accepts(t, pins, h.Addr, seeded.Public) {
		t.Error("the seeded key is still pinned")
	}
	if !accepts(t, pins, h.Addr, fh.newKey.Public) {
		t.Error("the new key is not pinned")
	}
}

// A run that dies after the install still reaches the host: both keys are
// pinned until the new one is proven.
func TestRotationStoppedHalfwayKeepsBothPins(t *testing.T) {
	fh, pins, seeded, h, r := setup(t)
	fh.failNext = "set -e\nrm -f /etc/ssh/ssh_host_" // the install
	if err := r.Act(context.Background(), h); err == nil {
		t.Fatal("no error")
	}
	if !accepts(t, pins, h.Addr, seeded.Public) || !accepts(t, pins, h.Addr, fh.newKey.Public) {
		t.Fatal("a half-done rotation must keep both keys pinned")
	}
}

// If the host does not serve the key it reported, the old pins stay and the
// stage fails.
func TestRotationFailsWhenTheHostServesAnotherKey(t *testing.T) {
	fh, pins, seeded, h, r := setup(t)
	impostor, _ := hostkey.Generate()
	r.Dial = func(ctx context.Context, addr string, cb ssh.HostKeyCallback) (io.Closer, error) {
		fh.serving = impostor.Public
		return fh.dial(ctx, addr, cb)
	}
	if err := r.Act(context.Background(), h); err == nil {
		t.Fatal("no error")
	}
	if !accepts(t, pins, h.Addr, seeded.Public) {
		t.Fatal("pins were changed before the new key was proven")
	}
}
