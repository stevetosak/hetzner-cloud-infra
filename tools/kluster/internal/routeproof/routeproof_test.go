package routeproof

import (
	"net/netip"
	"testing"
)

func TestCheckReadsTheLogin(t *testing.T) {
	p := Proof{User: "cp-dev", Hostname: "k8s-cp", ProbeIP: netip.MustParseAddr("10.100.0.254")}
	good := "cp-dev\nk8s-cp\n10.100.0.254 40512 10.100.0.1 22\nroot\n"
	if err := p.check(good); err != nil {
		t.Fatalf("a good login failed: %v", err)
	}
	bad := map[string]string{
		"public route": "cp-dev\nk8s-cp\n185.100.244.43 40512 95.217.1.1 22\nroot",
		"wrong user":   "root\nk8s-cp\n10.100.0.254 40512 10.100.0.1 22\nroot",
		"no sudo":      "cp-dev\nk8s-cp\n10.100.0.254 40512 10.100.0.1 22\ncp-dev",
		"wrong host":   "cp-dev\nk8swk1\n10.100.0.254 40512 10.100.0.1 22\nroot",
		"short":        "cp-dev",
	}
	for name, out := range bad {
		if err := p.check(out); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
