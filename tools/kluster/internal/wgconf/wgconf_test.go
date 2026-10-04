package wgconf

import (
	"net/netip"
	"strings"
	"testing"
)

var hubIP = netip.MustParseAddr("10.100.0.1")

const laptop = `[Interface]
# the operator's laptop
Address = 10.100.0.69/32
PrivateKey = c2VjcmV0

[Peer]
# hub
PublicKey = oldHub=
Endpoint = 46.62.209.249:51820
AllowedIPs = 10.100.0.0/24
PersistentKeepalive = 25

[Peer]
# somewhere else
PublicKey = other=
AllowedIPs = 192.168.7.0/24
`

func TestSetHubChangesOnlyTheHubPeer(t *testing.T) {
	got, err := SetHub(laptop, hubIP, HubPeer{PublicKey: "newHub=", Endpoint: "203.0.113.9:51820"})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(strings.Replace(laptop, "PublicKey = oldHub=", "PublicKey = newHub=", 1),
		"Endpoint = 46.62.209.249:51820", "Endpoint = 203.0.113.9:51820", 1)
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	h, err := Hub(got, hubIP)
	if err != nil || h.PublicKey != "newHub=" || h.Endpoint != "203.0.113.9:51820" {
		t.Fatalf("Hub read back %+v, %v", h, err)
	}
}

func TestSetHubAddsAMissingEndpoint(t *testing.T) {
	conf := RehearsalCopy("10.100.0.69/32", "10.100.0.0/24")
	got, err := SetHub(conf, hubIP, HubPeer{PublicKey: "k=", Endpoint: "198.51.100.4:51820"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "PublicKey = k=\nEndpoint = 198.51.100.4:51820\nAllowedIPs") {
		t.Fatalf("endpoint not added after the key:\n%s", got)
	}
}

func TestSetHubRefusesAmbiguousOrMissingHub(t *testing.T) {
	two := laptop + "\n[Peer]\nPublicKey = x=\nAllowedIPs = 10.0.0.0/8\n"
	if _, err := SetHub(two, hubIP, HubPeer{PublicKey: "k="}); err == nil {
		t.Error("two peers route the hub; want a refusal")
	}
	none := "[Interface]\nAddress = 10.100.0.69/32\n"
	if _, err := SetHub(none, hubIP, HubPeer{PublicKey: "k="}); err == nil {
		t.Error("no peer routes the hub; want a refusal")
	}
}
