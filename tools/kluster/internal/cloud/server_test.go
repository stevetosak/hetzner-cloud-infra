package cloud

import (
	"context"
	"testing"
)

func TestServerReadsTheAPI(t *testing.T) {
	f := &fakeAPI{}
	c := newFake(t, f)
	s, err := c.Server(context.Background(), "k8s-cp")
	if err != nil || s != nil {
		t.Fatalf("an empty slot must read as nil, got %+v, %v", s, err)
	}
	f.servers = `[{"id":42,"name":"k8s-cp","status":"running","protection":{"delete":true,"rebuild":true},
	  "public_net":{"ipv4":{"ip":"95.217.1.1"}},"private_net":[{"network":1,"ip":"10.0.1.5","mac_address":"86:00:00:26:21:d8"}]}]`
	s, err = c.Server(context.Background(), "k8s-cp")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "42" || s.PublicIP != "95.217.1.1" || len(s.PrivateIPs) != 1 || s.PrivateIPs[0] != "10.0.1.5" || s.PrivateMAC != "86:00:00:26:21:d8" ||
		!s.Running() || !s.DeleteProtected || !s.RebuildProtected {
		t.Fatalf("read %+v", s)
	}
}
