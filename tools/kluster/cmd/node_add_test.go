package cmd

import (
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stages"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

func nodeAddConfig() *config.Config {
	return &config.Config{
		ControlPlane: config.ControlPlane{Name: "k8s-cp", VpnIP: "10.100.0.1"},
		Workers:      config.Workers{ServerType: "cx23", PrivateIPs: "10.0.2.6-10.0.2.254", VpnIPs: "10.100.0.2-10.100.0.253"},
		WireGuard: config.WireGuard{ProbeIP: "10.100.0.254", Peers: []config.Peer{
			{Name: "admin-laptop", AllowedIPs: "10.100.0.3/32"},
		}},
	}
}

func TestCheckWorkerName(t *testing.T) {
	for name, ok := range map[string]bool{
		"k8swk4": true, "worker-1": true,
		"K8swk4": false, "-wk": false, "wk-": false, "wk_4": false, "k8s.wk": false, "": false,
		strings.Repeat("a", 64): false, "k8s-cp": false,
	} {
		if err := checkWorkerName(name, "k8s-cp"); (err == nil) != ok {
			t.Errorf("%q: err = %v, want ok %v", name, err, ok)
		}
	}
}

// A new Worker skips the addresses the set holds, and on the VPN also the
// hub and every operator peer.
func TestNextWorkerSkipsTakenAndPeerAddresses(t *testing.T) {
	set, err := workerset.Parse("t.tfvars", []byte(`workers = {
  k8swk1 = { private_ip = "10.0.2.6", vpn_ip = "10.100.0.2", server_type = "cx23", labels = {} }
}
`))
	if err != nil {
		t.Fatal(err)
	}
	w, err := nextWorker(nodeAddConfig(), set, "k8swk2", "cpx32")
	if err != nil {
		t.Fatal(err)
	}
	if w.PrivateIP != "10.0.2.7" || w.VpnIP != "10.100.0.4" || w.ServerType != "cpx32" {
		t.Fatalf("got %+v; want 10.0.2.7, 10.100.0.4 (10.100.0.3 is the laptop), cpx32", w)
	}
}

// --server-type picks a new Worker's type; without it the configured
// default holds.
func TestServerTypeFlagOverridesTheDefault(t *testing.T) {
	c := nodeAddConfig()
	if got := serverType(c, ""); got != "cx23" {
		t.Errorf("no flag: %s, want the default cx23", got)
	}
	if got := serverType(c, "cpx32"); got != "cpx32" {
		t.Errorf("--server-type cpx32: %s", got)
	}
}

// A declared Worker keeps its type: the flag may repeat it, never change it.
func TestCheckDeclaredTypeRefusesAnotherType(t *testing.T) {
	w := workerset.Worker{Name: "k8swk4", ServerType: "cpx32"}
	for flag, ok := range map[string]bool{"": true, "cpx32": true, "cx23": false} {
		if err := checkDeclaredType(w, flag, "t.tfvars"); (err == nil) != ok {
			t.Errorf("--server-type %q: err = %v, want ok %v", flag, err, ok)
		}
	}
}

func TestReservedVPNRefusesAWiderPeer(t *testing.T) {
	c := nodeAddConfig()
	c.WireGuard.Peers[0].AllowedIPs = "10.100.0.0/30"
	if _, err := reservedVPN(c); err == nil {
		t.Fatal("a peer holding several addresses must be refused")
	}
}

func TestNoNodeRefusesAStaleNode(t *testing.T) {
	nodes := []kube.Node{{Name: "k8s-cp"}, {Name: "k8swk4"}}
	if err := noNode(nodes, "k8swk4"); err == nil || !strings.Contains(err.Error(), "node replace") {
		t.Fatalf("got %v", err)
	}
	if err := noNode(nodes, "k8swk5"); err != nil {
		t.Fatal(err)
	}
}

func TestCheckNewWorker(t *testing.T) {
	w := workerset.Worker{Name: "k8swk4", PrivateIP: "10.0.2.9"}
	good := &cloud.Server{Name: "k8swk4", Status: "running", PublicIP: "203.0.113.9", PrivateIPs: []string{"10.0.2.9"}, PrivateMAC: "86:00:00:00:00:01"}
	if err := checkNewWorker(good, w); err != nil {
		t.Fatal(err)
	}
	bad := *good
	bad.PrivateIPs = []string{"10.0.2.10"}
	bad.Status = "starting"
	err := checkNewWorker(&bad, w)
	if err == nil || !strings.Contains(err.Error(), "10.0.2.9") || !strings.Contains(err.Error(), "running") {
		t.Fatalf("got %v", err)
	}
}

// Every provisioner joins through joinStages, so a machine whose Node the
// dead-node controller deleted comes back through node add: the join is
// skipped, and NodeRegistered restarts the kubelet and waits for the Node.
func TestJoinStagesBringBackADeletedNode(t *testing.T) {
	got := joinStages(nil, "k8s-api.tosak.net:6443", "k8swk3")
	if len(got) != 2 {
		t.Fatalf("joinStages = %d stages, want the join and NodeRegistered", len(got))
	}
	if j, ok := got[0].(stages.Join); !ok || j.Endpoint != "k8s-api.tosak.net:6443" {
		t.Errorf("stage 0 = %#v, want the Join to the endpoint", got[0])
	}
	r, ok := got[1].(stages.NodeRegistered)
	if !ok || r.Node != "k8swk3" || r.Timeout != registerTimeout || r.Every != nodePoll {
		t.Errorf("stage 1 = %#v, want NodeRegistered for k8swk3", got[1])
	}
}
