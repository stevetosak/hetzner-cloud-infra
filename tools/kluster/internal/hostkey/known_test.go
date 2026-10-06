package hostkey

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func writeKnownHosts(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func ecdsaKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func sameKey(a, b ssh.PublicKey) bool { return bytes.Equal(a.Marshal(), b.Marshal()) }

// The live Control Plane was built by hand: its address has keys of three
// types in the operator's file, and only the ed25519 one is pinned.
func TestFromKnownHostsTakesTheEd25519KeyOfThatAddress(t *testing.T) {
	ed, _ := Generate()
	other, _ := Generate()
	addr := "10.100.0.1"
	path := writeKnownHosts(t,
		knownhosts.Line([]string{addr}, ecdsaKey(t)),
		knownhosts.Line([]string{"10.100.0.2"}, other.Public),
		knownhosts.Line([]string{addr}, ed.Public),
	)
	got, err := FromKnownHosts(path, addr)
	if err != nil {
		t.Fatal(err)
	}
	if !sameKey(got, ed.Public) {
		t.Errorf("got %s, want the ed25519 key of %s", ssh.FingerprintSHA256(got), addr)
	}
}

func TestFromKnownHostsFindsHashedEntries(t *testing.T) {
	ed, _ := Generate()
	addr := "46.62.209.249"
	path := writeKnownHosts(t, knownhosts.Line([]string{knownhosts.HashHostname(knownhosts.Normalize(addr))}, ed.Public))
	got, err := FromKnownHosts(path, addr)
	if err != nil {
		t.Fatal(err)
	}
	if !sameKey(got, ed.Public) {
		t.Error("the hashed entry was not found")
	}
}

func TestFromKnownHostsRefuses(t *testing.T) {
	a, _ := Generate()
	b, _ := Generate()
	addr := "10.100.0.1"
	for name, tc := range map[string]struct {
		lines []string
		addr  string
		want  string
	}{
		"no entry":         {[]string{knownhosts.Line([]string{"10.100.0.2"}, a.Public)}, addr, "no ed25519 host key"},
		"only ecdsa":       {[]string{knownhosts.Line([]string{addr}, ecdsaKey(t))}, addr, "1 of other types"},
		"two ed25519 keys": {[]string{knownhosts.Line([]string{addr}, a.Public), knownhosts.Line([]string{addr}, b.Public)}, addr, "2 ed25519"},
		"a name":           {[]string{knownhosts.Line([]string{"k8s-cp"}, a.Public)}, "k8s-cp", "not an IP address"},
		"revoked":          {[]string{knownhosts.Line([]string{addr}, a.Public), "@revoked " + knownhosts.Line([]string{"*"}, a.Public)}, addr, "revoked"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := FromKnownHosts(writeKnownHosts(t, tc.lines...), tc.addr)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

func TestPinsKeys(t *testing.T) {
	pins, err := OpenPins(filepath.Join(t.TempDir(), "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	k, _ := Generate()
	if got, err := pins.Keys("10.100.0.1"); err != nil || len(got) != 0 {
		t.Fatalf("empty pins: %v, %v", got, err)
	}
	if err := pins.Set("10.100.0.1", k.Public); err != nil {
		t.Fatal(err)
	}
	got, err := pins.Keys("10.100.0.1")
	if err != nil || len(got) != 1 || !sameKey(got[0], k.Public) {
		t.Fatalf("after Set: %v, %v", got, err)
	}
}
