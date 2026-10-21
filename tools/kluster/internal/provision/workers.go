// Package provision creates servers with a seeded host key, so that kluster's
// first login to each one is verified (ADR 0009).
package provision

import (
	"context"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
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
// creates. A non-nil want names the Workers the caller means to create: a
// first plan that would create any other set is refused before anything is
// applied. Under --apply it applies that saved plan and pins each seeded key
// at the server's public address. It returns the created Workers' public
// addresses by name.
func CreateWorkers(ctx context.Context, m Module, want []string, pins *hostkey.Pins, mode tf.Mode, out io.Writer) (map[string]string, error) {
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
	if want != nil && !slices.Equal(names, slices.Sorted(slices.Values(want))) {
		return nil, fmt.Errorf("the workers plan would create %v, not %v: the Worker set declares another Worker with no server", names, want)
	}

	keys := map[string]*hostkey.KeyPair{}
	userData := map[string]string{}
	in := intent.Intent{Description: fmt.Sprintf("create Workers %v with seeded host keys", names)}
	for _, n := range names {
		if keys[n], userData[n], err = seed(); err != nil {
			return nil, err
		}
		in.Expectations = append(in.Expectations, intent.Expectation{
			Address: WorkerAddress(n), Actions: []intent.Action{intent.Create}, Required: true,
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
	return pinCreated(ctx, m, keys, pins, out)
}

// Replacer is the part of the workers Module a replacement drives.
type Replacer interface {
	Module
	PlanReplace(ctx context.Context, vars map[string]any, replace ...string) (*tf.Plan, error)
}

// ReplaceIntent is a replacement's plan: that one server destroyed and
// created again, and nothing else.
func ReplaceIntent(name string) intent.Intent {
	return intent.Intent{
		Description: fmt.Sprintf("replace Worker %s with a seeded host key", name),
		Expectations: []intent.Expectation{
			{Address: WorkerAddress(name), Actions: []intent.Action{intent.Replace}, Required: true},
		},
	}
}

// Replacement is one Worker's replacement, planned and checked against
// ReplaceIntent but not yet applied: the saved plan and the seeded key its
// user_data installs. node replace makes it before the Worker leaves the
// cluster, so a plan that would do anything else is refused while nothing
// has changed.
type Replacement struct {
	m    Replacer
	name string
	plan *tf.Plan
	key  *hostkey.KeyPair
}

// PlanReplace plans to destroy the named Worker's server and create it again
// under the same name, with a seeded host key in user_data, and checks the
// plan against ReplaceIntent. A mismatch warns in Plan Mode and returns
// tf.ErrIntent under --apply. The Module has no create_before_destroy, so
// the old server goes first: Hetzner names are unique.
func PlanReplace(ctx context.Context, m Replacer, name string, mode tf.Mode, out io.Writer) (*Replacement, error) {
	kp, ud, err := seed()
	if err != nil {
		return nil, err
	}
	p, err := m.PlanReplace(ctx, map[string]any{"user_data": map[string]string{name: ud}}, WorkerAddress(name))
	if err != nil {
		return nil, err
	}
	if err := tf.Check(p, ReplaceIntent(name), mode, out); err != nil {
		return nil, err
	}
	return &Replacement{m: m, name: name, plan: p, key: kp}, nil
}

// Apply applies the saved plan, not a new one: Terraform refuses it if the
// workers state changed since it was made. Under --apply it pins the seeded
// key at the new server's public address and returns that address; "" when
// nothing was applied.
func (r *Replacement) Apply(ctx context.Context, pins *hostkey.Pins, mode tf.Mode, out io.Writer) (string, error) {
	applied, err := tf.ApplyChecked(ctx, r.m, r.plan, mode, out)
	if err != nil || !applied {
		return "", err
	}
	created, err := pinCreated(ctx, r.m, map[string]*hostkey.KeyPair{r.name: r.key}, pins, out)
	if err != nil {
		return "", err
	}
	return created[r.name], nil
}

// WorkerAddress is the named Worker's server in the workers Module.
func WorkerAddress(name string) string {
	return fmt.Sprintf(`hcloud_server.workers[%q]`, name)
}

// seed makes a host key pair and the user_data that installs it.
func seed() (*hostkey.KeyPair, string, error) {
	kp, err := hostkey.Generate()
	if err != nil {
		return nil, "", err
	}
	ud, err := hostkey.UserData(kp)
	if err != nil {
		return nil, "", err
	}
	return kp, ud, nil
}

// pinCreated pins each new server's seeded key at its public address, read
// from the workers outputs, and returns those addresses by name.
func pinCreated(ctx context.Context, m Module, keys map[string]*hostkey.KeyPair, pins *hostkey.Pins, out io.Writer) (map[string]string, error) {
	var ips map[string]string
	if err := m.Output(ctx, "worker_public_ips", &ips); err != nil {
		return nil, err
	}
	created := map[string]string{}
	for _, n := range slices.Sorted(maps.Keys(keys)) {
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
