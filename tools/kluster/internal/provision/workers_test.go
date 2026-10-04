package provision

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
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
}

func (f *fakeWorkers) Plan(_ context.Context, vars map[string]any) (*tf.Plan, error) {
	f.lastVars = vars
	return &tf.Plan{Module: "workers", Changes: f.changes}, nil
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
	got, err := CreateWorkers(context.Background(), m, pins, tf.Mode{Apply: true}, io.Discard)
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
	_, err := CreateWorkers(context.Background(), m, newPins(t), tf.Mode{Apply: true}, io.Discard)
	if !errors.Is(err, tf.ErrIntent) {
		t.Fatalf("got %v", err)
	}
	if m.applied != nil {
		t.Fatal("applied")
	}
}
