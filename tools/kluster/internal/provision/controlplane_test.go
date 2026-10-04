package provision

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/hostkey"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/intent"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/tf"
)

var marker = hostkey.File{Path: "/etc/kluster/cp-init", Content: "running\n", Permissions: "0644"}

func TestCreateControlPlaneAppliesOneCreateWithSeededUserData(t *testing.T) {
	m := &fakeWorkers{changes: []intent.Change{{Address: ControlPlaneAddress, Action: intent.Create}}}
	kp, err := CreateControlPlane(context.Background(), m, []hostkey.File{marker}, tf.Mode{Apply: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if kp == nil || m.applied == nil {
		t.Fatal("nothing was applied")
	}
	ud, _ := m.applied["user_data"].(string)
	if !strings.Contains(ud, hostkey.AuthorizedKey(kp.Public)) || !strings.Contains(ud, "/etc/kluster/cp-init") {
		t.Fatalf("user_data lacks the seeded key or the marker:\n%s", ud)
	}
}

// ADR 0009: one create and no destroy, replace or update.
func TestCreateControlPlaneRefusesAnythingButOneCreate(t *testing.T) {
	plans := map[string][]intent.Change{
		"replace": {{Address: ControlPlaneAddress, Action: intent.Replace}},
		"update":  {{Address: ControlPlaneAddress, Action: intent.Update}},
		"no-op":   nil,
		"extra":   {{Address: ControlPlaneAddress, Action: intent.Create}, {Address: "hcloud_server.other", Action: intent.Create}},
	}
	for name, changes := range plans {
		t.Run(name, func(t *testing.T) {
			m := &fakeWorkers{changes: changes}
			kp, err := CreateControlPlane(context.Background(), m, nil, tf.Mode{Apply: true}, io.Discard)
			if !errors.Is(err, tf.ErrIntent) || kp != nil || m.applied != nil {
				t.Fatalf("want ErrIntent and nothing applied, got %v, applied %v", err, m.applied != nil)
			}
		})
	}
}

func TestCreateControlPlanePlanModeAppliesNothing(t *testing.T) {
	m := &fakeWorkers{changes: []intent.Change{{Address: ControlPlaneAddress, Action: intent.Create}}}
	kp, err := CreateControlPlane(context.Background(), m, nil, tf.Mode{}, io.Discard)
	if err != nil || kp != nil || m.applied != nil {
		t.Fatalf("Plan Mode applied: %v, %v", kp, err)
	}
}
