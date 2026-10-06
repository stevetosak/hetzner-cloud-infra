package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/hostkey"
)

func pinImportFixture(t *testing.T, addr string) (pinImportRun, *hostkey.KeyPair) {
	t.Helper()
	dir := t.TempDir()
	kp, err := hostkey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	kh := filepath.Join(dir, "operator_known_hosts")
	if err := os.WriteFile(kh, []byte(knownhosts.Line([]string{addr}, kp.Public)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return pinImportRun{Addr: addr, Subnet: "10.100.0.0/24", KnownHosts: kh, PinsPath: filepath.Join(dir, "pins"), Env: "live"}, kp
}

func TestPinImportPlanModeWritesNothing(t *testing.T) {
	r, kp := pinImportFixture(t, "10.100.0.1")
	var out bytes.Buffer
	if err := pinImport(r, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), hostkey.AuthorizedKey(kp.Public)) || !strings.Contains(out.String(), "SHA256:") {
		t.Errorf("Plan Mode must print the key and its fingerprint:\n%s", out.String())
	}
	if b, _ := os.ReadFile(r.PinsPath); len(b) != 0 {
		t.Errorf("Plan Mode wrote the pins: %q", b)
	}
}

func TestPinImportApplyPinsAndIsIdempotent(t *testing.T) {
	r, kp := pinImportFixture(t, "10.100.0.1")
	r.Apply = true
	var out bytes.Buffer
	if err := pinImport(r, &out); err != nil {
		t.Fatal(err)
	}
	pins, _ := hostkey.OpenPins(r.PinsPath)
	got, err := pins.Keys("10.100.0.1")
	if err != nil || len(got) != 1 || !bytes.Equal(got[0].Marshal(), kp.Public.Marshal()) {
		t.Fatalf("pins after --apply: %v, %v", got, err)
	}
	out.Reset()
	if err := pinImport(r, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing to do") {
		t.Errorf("a second import must change nothing:\n%s", out.String())
	}
}

// A pinned public address means a server kluster created: cp init would take
// the hand-built live Control Plane for a build of its own.
func TestPinImportRefusesAPublicAddress(t *testing.T) {
	r, _ := pinImportFixture(t, "46.62.209.249")
	r.Apply = true
	err := pinImport(r, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "outside the VPN subnet") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(r.PinsPath); len(b) != 0 {
		t.Errorf("the refused import wrote the pins: %q", b)
	}
}
