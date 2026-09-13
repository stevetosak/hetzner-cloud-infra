package cluster

import (
	"strings"
	"testing"
)

func TestNextAvailableVpnIP(t *testing.T) {
	cases := []struct {
		name     string
		subnet   string
		excluded []string
		want     string
		wantErr  bool
	}{
		{
			name:     "empty subnet returns first host after network address",
			subnet:   "10.100.0.0/24",
			excluded: nil,
			want:     "10.100.0.1",
		},
		{
			name:     "skips control plane and existing workers",
			subnet:   "10.100.0.0/24",
			excluded: []string{"10.100.0.1", "10.100.0.2", "10.100.0.3"},
			want:     "10.100.0.4",
		},
		{
			name:     "excluded entries carrying a /mask suffix are still matched",
			subnet:   "10.100.0.0/24",
			excluded: []string{"10.100.0.1", "10.100.0.69/32"},
			want:     "10.100.0.2",
		},
		{
			name:     "returns lowest gap, not just highest+1",
			subnet:   "10.100.0.0/24",
			excluded: []string{"10.100.0.1", "10.100.0.2", "10.100.0.4"},
			want:     "10.100.0.3",
		},
		{
			name:    "invalid CIDR errors",
			subnet:  "not-a-cidr",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NextAvailableVpnIP(tc.subnet, tc.excluded)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got ip=%q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("NextAvailableVpnIP(%q, %v) = %q, want %q", tc.subnet, tc.excluded, got, tc.want)
			}
		})
	}
}

func TestNextAvailableVpnIP_ExhaustedSubnet(t *testing.T) {
	// /30 has exactly 2 usable-ish addresses in our scheme (network, .1, .2, broadcast);
	// excluding both non-network/broadcast addresses should error.
	_, err := NextAvailableVpnIP("10.100.0.0/30", []string{"10.100.0.1", "10.100.0.2"})
	if err == nil {
		t.Fatal("expected error for exhausted subnet, got nil")
	}
}

func TestRebuildCPWgConfig(t *testing.T) {
	workers := []WorkerPeer{
		{Name: "1.2.3.4", VpnIP: "10.100.0.2", PublicKey: "worker1pubkey"},
		{Name: "1.2.3.5", VpnIP: "10.100.0.3", PublicKey: "worker2pubkey"},
	}
	staticPeers := []StaticPeer{
		{Name: "admin-laptop", PublicKey: "adminpubkey", AllowedIPs: "10.100.0.69/32"},
	}

	got := RebuildCPWgConfig("10.100.0.1", "cpprivatekey", 51820, workers, staticPeers)

	want := "[Interface]\n" +
		"Address = 10.100.0.1/24\n" +
		"PrivateKey = cpprivatekey\n" +
		"ListenPort = 51820\n" +
		"\n# 1.2.3.4\n[Peer]\nPublicKey = worker1pubkey\nAllowedIPs = 10.100.0.2/32\n" +
		"\n# 1.2.3.5\n[Peer]\nPublicKey = worker2pubkey\nAllowedIPs = 10.100.0.3/32\n" +
		"\n# admin-laptop\n[Peer]\nPublicKey = adminpubkey\nAllowedIPs = 10.100.0.69/32\n"

	if got != want {
		t.Errorf("RebuildCPWgConfig() =\n%q\nwant:\n%q", got, want)
	}
}

func TestAppendPeerBlock(t *testing.T) {
	existing := "[Interface]\nAddress = 10.100.0.1/24\nPrivateKey = cpkey\nListenPort = 51820\n"

	got := AppendPeerBlock(existing, "k8swk4", "newpubkey", "10.100.0.4/32")

	want := existing + "\n# k8swk4\n[Peer]\nPublicKey = newpubkey\nAllowedIPs = 10.100.0.4/32\n"
	if got != want {
		t.Errorf("AppendPeerBlock() =\n%q\nwant:\n%q", got, want)
	}
}

func TestRemovePeerBlock(t *testing.T) {
	full := RebuildCPWgConfig(
		"10.100.0.1", "cpkey", 51820,
		[]WorkerPeer{
			{Name: "node-a", VpnIP: "10.100.0.2", PublicKey: "keyA"},
			{Name: "node-b", VpnIP: "10.100.0.3", PublicKey: "keyB"},
		},
		[]StaticPeer{{Name: "admin", PublicKey: "adminkey", AllowedIPs: "10.100.0.69/32"}},
	)

	got := RemovePeerBlock(full, "10.100.0.3/32")

	if strings.Contains(got, "node-b") || strings.Contains(got, "keyB") {
		t.Errorf("RemovePeerBlock() left node-b's block in place:\n%s", got)
	}
	if !strings.Contains(got, "node-a") || !strings.Contains(got, "keyA") {
		t.Errorf("RemovePeerBlock() removed the wrong peer:\n%s", got)
	}
	if !strings.Contains(got, "admin") || !strings.Contains(got, "adminkey") {
		t.Errorf("RemovePeerBlock() removed the static peer:\n%s", got)
	}
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("RemovePeerBlock() left doubled blank lines:\n%q", got)
	}
}

func TestRemovePeerBlock_NoMatch(t *testing.T) {
	existing := "[Interface]\nAddress = 10.100.0.1/24\n\n# node-a\n[Peer]\nPublicKey = keyA\nAllowedIPs = 10.100.0.2/32\n"

	got := RemovePeerBlock(existing, "10.100.0.99/32")

	if got != existing {
		t.Errorf("RemovePeerBlock() with no matching peer changed the config:\ngot:  %q\nwant: %q", got, existing)
	}
}
