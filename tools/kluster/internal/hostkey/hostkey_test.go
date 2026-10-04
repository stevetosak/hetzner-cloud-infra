package hostkey

import (
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"gopkg.in/yaml.v3"
)

func TestUserDataInstallsExactlyTheKey(t *testing.T) {
	kp, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	ud, err := UserData(kp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ud, "#cloud-config\n") {
		t.Fatal("cloud-init ignores user data without the #cloud-config header")
	}
	var cc cloudConfig
	if err := yaml.Unmarshal([]byte(ud), &cc); err != nil {
		t.Fatal(err)
	}
	if !cc.SSHDeleteKeys || cc.SSHGenKeyTypes == nil || len(cc.SSHGenKeyTypes) != 0 {
		t.Errorf("image keys survive or new ones are generated: %+v", cc)
	}
	if !strings.Contains(ud, "ssh_genkeytypes: []") {
		t.Error("ssh_genkeytypes must render as an empty list, not be omitted")
	}
	// The private half in the user data must be the one whose public half is pinned.
	signer, err := ssh.ParsePrivateKey([]byte(cc.SSHKeys["ed25519_private"]))
	if err != nil {
		t.Fatal(err)
	}
	if AuthorizedKey(signer.PublicKey()) != AuthorizedKey(kp.Public) || cc.SSHKeys["ed25519_public"] != AuthorizedKey(kp.Public) {
		t.Error("the private and public halves do not match")
	}
	if strings.Contains(ud, "write_files") {
		t.Error("write_files rendered with no file")
	}
}

func TestUserDataWritesFiles(t *testing.T) {
	kp, _ := Generate()
	ud, err := UserData(kp, File{Path: "/etc/kluster/cp-init", Content: "running\n", Permissions: "0644"})
	if err != nil {
		t.Fatal(err)
	}
	var cc cloudConfig
	if err := yaml.Unmarshal([]byte(ud), &cc); err != nil {
		t.Fatal(err)
	}
	if len(cc.WriteFiles) != 1 || cc.WriteFiles[0] != (File{"/etc/kluster/cp-init", "running\n", "0644"}) {
		t.Fatalf("write_files: %+v\n%s", cc.WriteFiles, ud)
	}
}

func TestPinsVerifyAndReplace(t *testing.T) {
	pins, err := OpenPins(filepath.Join(t.TempDir(), "env", "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	seeded, _ := Generate()
	rotated, _ := Generate()
	other, _ := Generate()
	addr := "203.0.113.7"
	remote := &net.TCPAddr{IP: net.ParseIP(addr), Port: 22}

	check := func(k ssh.PublicKey) error {
		cb, err := pins.Callback()
		if err != nil {
			t.Fatal(err)
		}
		return cb(HostPort(addr), remote, k)
	}

	var keyErr *knownhosts.KeyError
	if err := check(seeded.Public); !errors.As(err, &keyErr) || len(keyErr.Want) != 0 {
		t.Fatalf("an unpinned address must be refused, not learned: %v", err)
	}
	if has, _ := pins.Has(addr); has {
		t.Fatal("Has reports a pin that was never set")
	}
	if err := pins.Set(addr, seeded.Public); err != nil {
		t.Fatal(err)
	}
	if has, _ := pins.Has(addr); !has {
		t.Fatal("Has misses a pin that was set")
	}
	if err := check(seeded.Public); err != nil {
		t.Fatalf("pinned key refused: %v", err)
	}
	if err := check(other.Public); !errors.As(err, &keyErr) {
		t.Fatalf("a wrong key was accepted: %v", err)
	}

	// Mid-rotation both keys are accepted; afterwards only the new one.
	if err := pins.Add(addr, rotated.Public); err != nil {
		t.Fatal(err)
	}
	if check(seeded.Public) != nil || check(rotated.Public) != nil {
		t.Fatal("mid-rotation, either key must pass")
	}
	if err := pins.Set(addr, rotated.Public); err != nil {
		t.Fatal(err)
	}
	if check(seeded.Public) == nil {
		t.Fatal("the seeded key still passes after the rotation")
	}
	if err := check(rotated.Public); err != nil {
		t.Fatal(err)
	}
}
