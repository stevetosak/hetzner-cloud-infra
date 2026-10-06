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

const hub = `[Interface]
Address    = 10.100.0.1/24
ListenPort = 51820
PrivateKey = c2VjcmV0

[Peer]
# admin-laptop
PublicKey  = laptop=
AllowedIPs = 10.100.0.69/32

[Peer]
# k8swk1
PublicKey  = wk1=
AllowedIPs = 10.100.0.2/32`

func TestAddPeerAppendsAndKeepsEveryLine(t *testing.T) {
	got, err := AddPeer(hub, Peer{Name: "k8swk2", PublicKey: "wk2=", AllowedIPs: "10.100.0.3/32"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, hub+"\n") {
		t.Fatalf("existing lines changed:\n%s", got)
	}
	ps := Peers(got)
	if len(ps) != 3 || ps[2] != (Peer{Name: "k8swk2", PublicKey: "wk2=", AllowedIPs: "10.100.0.3/32"}) {
		t.Fatalf("peers %+v", ps)
	}
}

func TestAddPeerRefusesATakenKeyOrAddress(t *testing.T) {
	for _, p := range []Peer{
		{Name: "x", PublicKey: "wk1=", AllowedIPs: "10.100.0.9/32"},
		{Name: "x", PublicKey: "new=", AllowedIPs: "10.100.0.2/32"},
	} {
		if _, err := AddPeer(hub, p); err == nil {
			t.Errorf("AddPeer(%+v) accepted a clash", p)
		}
	}
}

func TestRemovePeerUndoesAddPeer(t *testing.T) {
	added, err := AddPeer(hub, Peer{Name: "k8swk2", PublicKey: "wk2=", AllowedIPs: "10.100.0.3/32"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := RemovePeer(added, "wk2=")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimRight(got, "\n") != hub {
		t.Fatalf("got\n%s", got)
	}
	if _, err := RemovePeer(hub, "absent="); err == nil {
		t.Fatal("removed a peer that is not there")
	}
}

func TestRemovePeerKeepsTheOthers(t *testing.T) {
	got, err := RemovePeer(hub, "laptop=")
	if err != nil {
		t.Fatal(err)
	}
	ps := Peers(got)
	if len(ps) != 1 || ps[0].Name != "k8swk1" || !strings.Contains(got, "PrivateKey = c2VjcmV0") {
		t.Fatalf("got\n%s", got)
	}
}
