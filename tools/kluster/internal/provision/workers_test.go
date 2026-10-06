package provision

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/hostkey"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/intent"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/tf"
)

// fakeWorkers plans the same changes whatever the vars, and records the
// user_data of the plan it was asked to apply.
type fakeWorkers struct {
	changes  []intent.Change
	lastVars map[string]any
	applied  map[string]any
	ips      map[string]string
	replaced []string
}

func (f *fakeWorkers) Plan(_ context.Context, vars map[string]any) (*tf.Plan, error) {
	f.lastVars = vars
	return &tf.Plan{Module: "workers", Changes: f.changes}, nil
}

func (f *fakeWorkers) PlanReplace(ctx context.Context, vars map[string]any, replace ...string) (*tf.Plan, error) {
	f.replaced = replace
	return f.Plan(ctx, vars)
}

func (f *fakeWorkers) Apply(context.Context, *tf.Plan) error { f.applied = f.lastVars; return nil }

func (f *fakeWorkers) Output(_ context.Context, name string, v any) error {
	if name != "worker_public_ips" {
		return errors.New("unknown output")
	}
	b, _ := json.Marshal(f.ips)
	return json.Unmarshal(b, v)
}

func newPins(t *testing.T) *hostkey.Pins {
	t.Helper()
	p, err := hostkey.OpenPins(filepath.Join(t.TempDir(), "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCreateWorkersSeedsAndPinsEachNewServer(t *testing.T) {
	m := &fakeWorkers{
		changes: []intent.Change{
			{Address: `hcloud_server.workers["k8swk1"]`, Action: intent.Create},
			{Address: `hcloud_server.workers["k8swk2"]`, Action: intent.Create},
		},
		ips: map[string]string{"k8swk1": "203.0.113.1", "k8swk2": "203.0.113.2"},
	}
	pins := newPins(t)
	got, err := CreateWorkers(context.Background(), m, nil, pins, tf.Mode{Apply: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["k8swk2"] != "203.0.113.2" {
		t.Fatalf("created = %v", got)
	}

	ud := m.applied["user_data"].(map[string]string)
	cb, _ := pins.Callback()
	for name, ip := range got {
		// The pinned key is the public half of the key in that server's user data.
		var cc struct {
			SSHKeys map[string]string `yaml:"ssh_keys"`
		}
		if err := yaml.Unmarshal([]byte(ud[name]), &cc); err != nil {
			t.Fatal(err)
		}
		k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(cc.SSHKeys["ed25519_public"]))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := cb(ip+":22", &net.TCPAddr{IP: net.ParseIP(ip), Port: 22}, k); err != nil {
			t.Errorf("%s: seeded key not pinned at %s: %v", name, ip, err)
		}
	}
	if ud["k8swk1"] == ud["k8swk2"] {
		t.Error("two servers share a host key")
	}
}

// A plan that would also replace a live Worker aborts before anything is
// applied or pinned.
func TestCreateWorkersRefusesAnyOtherChange(t *testing.T) {
	m := &fakeWorkers{changes: []intent.Change{
		{Address: `hcloud_server.workers["k8swk4"]`, Action: intent.Create},
		{Address: `hcloud_server.workers["k8swk1"]`, Action: intent.Replace},
	}}
	_, err := CreateWorkers(context.Background(), m, nil, newPins(t), tf.Mode{Apply: true}, io.Discard)
	if !errors.Is(err, tf.ErrIntent) {
		t.Fatalf("got %v", err)
	}
	if m.applied != nil {
		t.Fatal("applied")
	}
}

// node add names its one Worker: a plan that would also create another
// declared Worker is refused before any key is made or anything applied.
func TestCreateWorkersRefusesCreatesBeyondWant(t *testing.T) {
	m := &fakeWorkers{changes: []intent.Change{
		{Address: `hcloud_server.workers["k8swk4"]`, Action: intent.Create},
		{Address: `hcloud_server.workers["k8swk5"]`, Action: intent.Create},
	}}
	_, err := CreateWorkers(context.Background(), m, []string{"k8swk4"}, newPins(t), tf.Mode{Apply: true}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "k8swk5") {
		t.Fatalf("got %v", err)
	}
	if m.applied != nil || m.lastVars != nil {
		t.Fatal("planned with keys or applied")
	}
}

// A replacement plans -replace of that one server with a new seeded key,
// and pins that key at the new server's address.
func TestReplaceWorkerSeedsAndPinsTheNewServer(t *testing.T) {
	addr := `hcloud_server.workers["k8swk2"]`
	m := &fakeWorkers{
		changes: []intent.Change{{Address: addr, Action: intent.Replace}},
		ips:     map[string]string{"k8swk1": "203.0.113.1", "k8swk2": "203.0.113.9"},
	}
	pins := newPins(t)
	ip, err := ReplaceWorker(context.Background(), m, "k8swk2", pins, tf.Mode{Apply: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if ip != "203.0.113.9" || len(m.replaced) != 1 || m.replaced[0] != addr {
		t.Fatalf("ip %q, -replace %v", ip, m.replaced)
	}
	ud := m.applied["user_data"].(map[string]string)
	if len(ud) != 1 || ud["k8swk2"] == "" {
		t.Fatalf("user_data for %v, want k8swk2 only", ud)
	}
	if ok, _ := pins.Has("203.0.113.9"); !ok {
		t.Fatal("the new server's key is not pinned")
	}
	if ok, _ := pins.Has("203.0.113.1"); ok {
		t.Fatal("pinned another Worker")
	}
}

// Anything but one replace of that server aborts before anything is
// applied or pinned: a delete alone, an update in place, a second Worker.
func TestReplaceWorkerRefusesAnyOtherPlan(t *testing.T) {
	addr := `hcloud_server.workers["k8swk2"]`
	for name, changes := range map[string][]intent.Change{
		"no-op":          nil,
		"delete only":    {{Address: addr, Action: intent.Delete}},
		"update":         {{Address: addr, Action: intent.Update}},
		"another Worker": {{Address: addr, Action: intent.Replace}, {Address: `hcloud_server.workers["k8swk1"]`, Action: intent.Replace}},
	} {
		t.Run(name, func(t *testing.T) {
			m := &fakeWorkers{changes: changes, ips: map[string]string{"k8swk2": "203.0.113.9"}}
			pins := newPins(t)
			_, err := ReplaceWorker(context.Background(), m, "k8swk2", pins, tf.Mode{Apply: true}, io.Discard)
			if !errors.Is(err, tf.ErrIntent) || m.applied != nil {
				t.Fatalf("want ErrIntent and nothing applied, got %v", err)
			}
		})
	}
}
