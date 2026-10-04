package filediff

import (
	"strings"
	"testing"
)

func TestUnifiedRedactsSecretsButShowsTheyChanged(t *testing.T) {
	old := "[Interface]\nPrivateKey = oldsecretAAAA=\n\n[Peer]\nPublicKey = hubA=\n"
	new := "[Interface]\nPrivateKey = newsecretBBBB=\n\n[Peer]\nPublicKey = hubB=\n"
	d := Unified("/etc/wireguard/wg0.conf", old, new)
	for _, secret := range []string{"oldsecretAAAA", "newsecretBBBB"} {
		if strings.Contains(d, secret) {
			t.Fatalf("diff shows a secret:\n%s", d)
		}
	}
	for _, want := range []string{"-PublicKey = hubA=", "+PublicKey = hubB=", "-PrivateKey = <redacted sha256:", "+PrivateKey = <redacted sha256:"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff lacks %q:\n%s", want, d)
		}
	}
}

func TestUnifiedRedactsKubeconfigKeys(t *testing.T) {
	d := Unified("kubeconfig", "users: []\n", "users:\n- name: a\n  user:\n    client-key-data: LS0tS0VZ\n    token: abc.def\n")
	if strings.Contains(d, "LS0tS0VZ") || strings.Contains(d, "abc.def") {
		t.Fatalf("diff shows a secret:\n%s", d)
	}
}

func TestUnifiedEmptyWhenEqual(t *testing.T) {
	if d := Unified("f", "a\n", "a\n"); d != "" {
		t.Fatalf("want no diff, got %q", d)
	}
}
