package vpn

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

func TestKeyRoundTrip(t *testing.T) {
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseKey(k.String())
	if err != nil || back != k {
		t.Fatalf("ParseKey(String()) = %v, %v", back, err)
	}
	if k.Public() == k || k.Public() == (Key{}) {
		t.Fatal("public key is not derived")
	}
	if _, err := ParseKey("short"); err == nil {
		t.Fatal("ParseKey accepted a non-key")
	}
}

// startHub runs a second userspace WireGuard device on loopback, standing in
// for the Control Plane's wg0, with a TCP echo server on hubIP:22.
func startHub(t *testing.T, hubPriv, peerPub Key, hubIP, peerIP netip.Addr) uint16 {
	t.Helper()
	tun, tnet, err := netstack.CreateNetTUN([]netip.Addr{hubIP}, nil, MTU)
	if err != nil {
		t.Fatal(err)
	}
	dev := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	t.Cleanup(dev.Close)
	cfg := fmt.Sprintf("private_key=%s\nlisten_port=0\npublic_key=%s\nallowed_ip=%s/32\n", hubPriv.hex(), peerPub.hex(), peerIP)
	if err := dev.IpcSet(cfg); err != nil {
		t.Fatal(err)
	}
	if err := dev.Up(); err != nil {
		t.Fatal(err)
	}
	l, err := tnet.ListenTCP(&net.TCPAddr{IP: hubIP.AsSlice(), Port: 22})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	got, err := dev.IpcGet()
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(got, "\n") {
		if v, ok := strings.CutPrefix(l, "listen_port="); ok {
			port, err := strconv.ParseUint(v, 10, 16)
			if err != nil {
				t.Fatal(err)
			}
			return uint16(port)
		}
	}
	t.Fatalf("the hub reports no port:\n%s", got)
	return 0
}

func TestTunnelReachesTheHub(t *testing.T) {
	hubPriv, _ := GenerateKey()
	peerPriv, _ := GenerateKey()
	hubIP, peerIP := netip.MustParseAddr("10.100.0.1"), netip.MustParseAddr("10.100.0.254")
	port := startHub(t, hubPriv, peerPriv.Public(), hubIP, peerIP)

	tun, err := Open(peerPriv, peerIP, Peer{
		PublicKey:  hubPriv.Public(),
		Endpoint:   netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port),
		AllowedIPs: []netip.Prefix{netip.PrefixFrom(hubIP, 32)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := tun.DialContext(ctx, "tcp", "10.100.0.1:22")
	if err != nil {
		t.Fatalf("dial through the tunnel: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo through the tunnel: %q, %v", buf, err)
	}
}

func TestTunnelRefusedByAHubThatDoesNotKnowThePeer(t *testing.T) {
	hubPriv, _ := GenerateKey()
	peerPriv, _ := GenerateKey()
	stranger, _ := GenerateKey()
	hubIP, peerIP := netip.MustParseAddr("10.100.0.1"), netip.MustParseAddr("10.100.0.254")
	port := startHub(t, hubPriv, stranger.Public(), hubIP, peerIP)

	tun, err := Open(peerPriv, peerIP, Peer{
		PublicKey:  hubPriv.Public(),
		Endpoint:   netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port),
		AllowedIPs: []netip.Prefix{netip.PrefixFrom(hubIP, 32)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tun.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if c, err := tun.DialContext(ctx, "tcp", "10.100.0.1:22"); err == nil {
		c.Close()
		t.Fatal("a hub that does not know the peer answered")
	}
}
