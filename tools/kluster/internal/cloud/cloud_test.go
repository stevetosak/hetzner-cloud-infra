package cloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// fakeAPI serves the few Hetzner endpoints these tests need. Actions come
// back already finished, so WaitFor never polls.
type fakeAPI struct {
	firewallRules string // JSON array of rules for firewall 1
	setRulesBody  string
	primaryIPs    string // JSON array
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/firewalls":
		io.WriteString(w, `{"firewalls":[{"id":1,"name":"`+r.URL.Query().Get("name")+`","rules":`+f.firewallRules+`}],"meta":{"pagination":{"page":1,"per_page":25,"total_entries":1}}}`)
	case r.Method == http.MethodPost && r.URL.Path == "/firewalls/1/actions/set_rules":
		b, _ := io.ReadAll(r.Body)
		f.setRulesBody = string(b)
		io.WriteString(w, `{"actions":[{"id":7,"command":"set_firewall_rules","status":"success","progress":100}]}`)
	case r.Method == http.MethodGet && r.URL.Path == "/primary_ips":
		io.WriteString(w, `{"primary_ips":`+f.primaryIPs+`,"meta":{"pagination":{"page":1,"per_page":50,"total_entries":1}}}`)
	case r.Method == http.MethodDelete && r.URL.Path == "/primary_ips/9":
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"code":"not_found","message":"primary ip not found"}}`)
	case r.Method == http.MethodDelete && r.URL.Path == "/ssh_keys/3":
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && r.URL.Path == "/servers":
		io.WriteString(w, `{"servers":[],"meta":{"pagination":{"page":1,"per_page":50,"total_entries":0}}}`)
	default:
		http.Error(w, `{"error":{"code":"not_found","message":"`+r.URL.Path+`"}}`, http.StatusNotFound)
	}
}

func newFake(t *testing.T, f *fakeAPI) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return New("test-token", hcloud.WithEndpoint(srv.URL))
}

const cpRules = `[
  {"direction":"in","protocol":"udp","port":"51820","source_ips":["0.0.0.0/0"],"destination_ips":[]},
  {"direction":"in","protocol":"tcp","port":"22","source_ips":["185.100.244.43/32"],"destination_ips":[]}
]`

// The by-hand fix from the workers runbook, step 9, on the Control Plane
// firewall: port 22 goes, the WireGuard rule stays.
func TestClearSSHKeepsWireGuard(t *testing.T) {
	f := &fakeAPI{firewallRules: cpRules}
	c := newFake(t, f)
	ctx := context.Background()

	open, err := c.SSHOpen(ctx, "tosak-cp-firewall")
	if err != nil || !open {
		t.Fatalf("SSHOpen = %v, %v; want true", open, err)
	}
	if err := c.ClearSSH(ctx, "tosak-cp-firewall"); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Rules []struct {
			Protocol string `json:"protocol"`
			Port     string `json:"port"`
		} `json:"rules"`
	}
	if err := json.Unmarshal([]byte(f.setRulesBody), &body); err != nil {
		t.Fatalf("set_rules body %q: %v", f.setRulesBody, err)
	}
	if len(body.Rules) != 1 || body.Rules[0].Port != "51820" {
		t.Fatalf("set_rules sent %s", f.setRulesBody)
	}
}

// An empty rule set must be sent as [] — Hetzner reads a missing or null
// list differently, and the worker firewall's steady state is no rules.
func TestClearSSHSendsAnEmptyList(t *testing.T) {
	f := &fakeAPI{firewallRules: `[{"direction":"in","protocol":"tcp","port":"22","source_ips":["1.2.3.4/32"],"destination_ips":[]}]`}
	c := newFake(t, f)
	if err := c.ClearSSH(context.Background(), "tosak-worker-firewall"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.setRulesBody, `"rules":[]`) {
		t.Fatalf("set_rules sent %s", f.setRulesBody)
	}
}

func TestGuardRefusesTheLiveProject(t *testing.T) {
	f := &fakeAPI{primaryIPs: `[{"id":1,"name":"tosak-cp-ip","ip":"46.62.209.249","type":"ipv4"}]`}
	c := newFake(t, f)
	err := c.GuardNotProject(context.Background(), "46.62.209.249")
	if err == nil || !strings.Contains(err.Error(), "live project") {
		t.Fatalf("guard let the live project through: %v", err)
	}
	f.primaryIPs = `[{"id":2,"name":"tosak-cp-ip","ip":"95.217.1.1","type":"ipv4"}]`
	if err := c.GuardNotProject(context.Background(), "46.62.209.249"); err != nil {
		t.Fatalf("guard refused a rehearsal project: %v", err)
	}
}

func TestIsSSHRule(t *testing.T) {
	port := func(p string) *string { return &p }
	tests := []struct {
		r    hcloud.FirewallRule
		want bool
	}{
		{hcloud.FirewallRule{Direction: "in", Protocol: "tcp", Port: port("22")}, true},
		{hcloud.FirewallRule{Direction: "in", Protocol: "udp", Port: port("22")}, false},
		{hcloud.FirewallRule{Direction: "out", Protocol: "tcp", Port: port("22")}, false},
		{hcloud.FirewallRule{Direction: "in", Protocol: "tcp", Port: port("2222")}, false},
		{hcloud.FirewallRule{Direction: "in", Protocol: "icmp"}, false},
	}
	for _, tt := range tests {
		if got := IsSSHRule(tt.r); got != tt.want {
			t.Errorf("IsSSHRule(%+v) = %v", tt.r, got)
		}
	}
}

// A server's auto-deleted primary IP is gone by the time down reaches it:
// that is done, not an error.
func TestDeleteAllTreatsNotFoundAsDeleted(t *testing.T) {
	c := newFake(t, &fakeAPI{})
	inv := &Inventory{
		PrimaryIPs: []*hcloud.PrimaryIP{{ID: 9, Name: "primary_ip-9"}},
		SSHKeys:    []*hcloud.SSHKey{{ID: 3, Name: "tosak-cluster"}},
	}
	var log strings.Builder
	if err := c.DeleteAll(context.Background(), inv, &log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "already gone: primary IP primary_ip-9") ||
		!strings.Contains(log.String(), "deleted ssh key tosak-cluster") {
		t.Fatalf("log:\n%s", log.String())
	}
}
