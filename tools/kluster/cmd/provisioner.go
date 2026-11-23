package cmd

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/nodeset"
)

// provisioner is how the node commands treat one kind of machine (ADR 0016):
// hcloud creates a Hetzner server with Terraform, byo takes a machine the
// operator installed. The host Stages are shared; how a machine is made,
// reached and checked comes from its provisioner. A new kind is one more
// type in provisioners.
type provisioner interface {
	// add declares a new machine on node add's first run, and builds and
	// joins a declared one on the second.
	add(ctx context.Context, a *app, name string) error
	remove(ctx context.Context, a *app, name string) error
	replace(ctx context.Context, a *app, name string) error
}

var provisioners = map[string]provisioner{
	nodeset.ProvisionerHcloud: hcloudProvisioner{},
	nodeset.ProvisionerByo:    byoProvisioner{},
}

// provisionerFor is the provisioner of a machine: the one the Node set names
// for it, else the one asked for, else hcloud, whose machines the Worker set
// holds. A machine's provisioner is never taken from its site's provider: one
// site can hold machines of two. Asking for another than the Node set names
// is refused.
func provisionerFor(set *nodeset.Set, name, asked string) (provisioner, string, error) {
	declared := ""
	if m := set.Mesh(); m != nil {
		if machine, ok := m.Machine(name); ok {
			declared = machine.Provisioner
		}
	}
	if asked != "" && declared != "" && asked != declared {
		return nil, "", fmt.Errorf("%s declares %s with provisioner %s, not %s", set.Path, name, declared, asked)
	}
	kind := cmp.Or(declared, asked, nodeset.ProvisionerHcloud)
	p, ok := provisioners[kind]
	if !ok {
		return nil, "", fmt.Errorf("provisioner %q is not one of %v", kind, slices.Sorted(maps.Keys(provisioners)))
	}
	return p, kind, nil
}

// provisionerOf is the provisioner of a machine node remove or replace
// names.
func (a *app) provisionerOf(name string) (provisioner, error) {
	set, err := a.nodeSet()
	if err != nil {
		return nil, err
	}
	p, _, err := provisionerFor(set, name, "")
	return p, err
}

// hcloudProvisioner is a Hetzner server of the Worker set, created with
// Terraform (docs/runbook/workers.md).
type hcloudProvisioner struct{}

func (hcloudProvisioner) add(ctx context.Context, a *app, name string) error {
	return nodeAddHcloud(ctx, a, name)
}

func (hcloudProvisioner) remove(ctx context.Context, a *app, name string) error {
	return nodeRemoveHcloud(ctx, a, name)
}

func (hcloudProvisioner) replace(ctx context.Context, a *app, name string) error {
	return nodeReplaceHcloud(ctx, a, name)
}

// byoProvisioner is a machine the operator installed and kluster builds over
// SSH (docs/runbook/workers.md#add-a-bring-your-own-machine).
type byoProvisioner struct{}

func (byoProvisioner) add(ctx context.Context, a *app, name string) error {
	return byoAdd(ctx, a, name)
}

func (byoProvisioner) remove(_ context.Context, _ *app, name string) error {
	return byoNotRemovable("remove", name)
}

func (byoProvisioner) replace(_ context.Context, _ *app, name string) error {
	return byoNotRemovable("replace", name)
}

// byoNotRemovable refuses node remove and replace on a byo machine, and names
// the steps by hand until kluster takes them.
func byoNotRemovable(command, name string) error {
	return fmt.Errorf("%[2]s is a byo machine, and node %[1]s does not take one out yet (a step of the Providers Plan). "+
		"By hand, as in docs/runbook/workers.md#take-a-bring-your-own-machine-out: kubectl drain %[2]s --ignore-daemonsets "+
		"--delete-emptydir-data; kubectl delete node %[2]s; on the machine, kubeadm reset -f; remove its data key from "+
		"kube-system/mesh-peers and its entry from the Node set; then kluster node mesh --apply", command, name)
}
