// Package provision creates servers with a seeded host key, so that kluster's
// first login to each one is verified (ADR 0009).
package provision

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/hostkey"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/intent"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/tf"
)

// Module is the part of the workers Module provisioning drives.
type Module interface {
	tf.Applier
	Plan(ctx context.Context, vars map[string]any) (*tf.Plan, error)
	Output(ctx context.Context, name string, v any) error
}

var workerAddress = regexp.MustCompile(`^hcloud_server\.workers\["([^"]+)"\]$`)

// CreateWorkers creates the Workers the workers Module would create, and only
// those. It plans once to learn which servers are new, makes a host key for
// each, plans again with the keys in user_data, and requires exactly those
// creates. Under --apply it applies that saved plan and pins each seeded key
// at the server's public address. It returns the created Workers' public
// addresses by name.
func CreateWorkers(ctx context.Context, m Module, pins *hostkey.Pins, mode tf.Mode, out io.Writer) (map[string]string, error) {
	first, err := m.Plan(ctx, nil)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, c := range first.Changes {
		if s := workerAddress.FindStringSubmatch(c.Address); s != nil && c.Action == intent.Create {
			names = append(names, s[1])
		}
	}
	sort.Strings(names)

	keys := map[string]*hostkey.KeyPair{}
	userData := map[string]string{}
	in := intent.Intent{Description: fmt.Sprintf("create Workers %v with seeded host keys", names)}
	for _, n := range names {
		kp, err := hostkey.Generate()
		if err != nil {
			return nil, err
		}
		ud, err := hostkey.UserData(kp)
		if err != nil {
			return nil, err
		}
		keys[n], userData[n] = kp, ud
		in.Expectations = append(in.Expectations, intent.Expectation{
			Address: fmt.Sprintf(`hcloud_server.workers[%q]`, n), Actions: []intent.Action{intent.Create}, Required: true,
		})
	}

	p, err := m.Plan(ctx, map[string]any{"user_data": userData})
	if err != nil {
		return nil, err
	}
	applied, err := tf.Decide(ctx, m, p, in, mode, out)
	if err != nil || !applied {
		return nil, err
	}

	var ips map[string]string
	if err := m.Output(ctx, "worker_public_ips", &ips); err != nil {
		return nil, err
	}
	created := map[string]string{}
	for _, n := range names {
		ip, ok := ips[n]
		if !ok || ip == "" {
			return nil, fmt.Errorf("worker %s has no public address in the workers outputs", n)
		}
		if err := pins.Set(ip, keys[n].Public); err != nil {
			return nil, err
		}
		fmt.Fprintf(out, "pinned the seeded host key of %s at %s\n", n, ip)
		created[n] = ip
	}
	return created, nil
}
