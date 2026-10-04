package provision

import (
	"context"
	"io"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/hostkey"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/intent"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/tf"
)

// ControlPlaneAddress is the control-plane Module's one server.
const ControlPlaneAddress = "hcloud_server.control_plane"

// ControlPlaneIntent is `cp init`'s Intent for the control-plane Module: one
// create, and no destroy, replace or update (ADR 0009).
var ControlPlaneIntent = intent.Intent{
	Description: "create the Control Plane into an empty slot",
	Expectations: []intent.Expectation{
		{Address: ControlPlaneAddress, Actions: []intent.Action{intent.Create}, Required: true},
	},
}

// CreateControlPlane plans the control-plane Module with a seeded host key
// and files in user_data, requires ControlPlaneIntent, and under --apply
// applies that saved plan. It returns the seeded key pair, or nil when
// nothing was applied. The caller reads the new server over the API and pins
// the key at its address.
func CreateControlPlane(ctx context.Context, m Module, files []hostkey.File, mode tf.Mode, out io.Writer) (*hostkey.KeyPair, error) {
	kp, err := hostkey.Generate()
	if err != nil {
		return nil, err
	}
	ud, err := hostkey.UserData(kp, files...)
	if err != nil {
		return nil, err
	}
	p, err := m.Plan(ctx, map[string]any{"user_data": ud})
	if err != nil {
		return nil, err
	}
	applied, err := tf.Decide(ctx, m, p, ControlPlaneIntent, mode, out)
	if err != nil || !applied {
		return nil, err
	}
	return kp, nil
}
