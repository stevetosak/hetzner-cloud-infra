package stages

import (
	"context"
	"fmt"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/nodeset"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// NodeProfile gives a Node the capability profile of its site as admin
// labels, on the Control Plane, and removes every other label under
// nodeset.Prefix (ADR 0016, Amendments). Labels outside the prefix belong to
// someone else and stay.
type NodeProfile struct {
	Node string
	Site nodeset.Site
}

func (NodeProfile) Name() string    { return "node-profile" }
func (NodeProfile) Runbook() string { return "docs/runbook/workers.md#set-the-node-profile" }

// change is what the Node's labels still lack. A missing Node is an error:
// the Stage runs only after the Node has joined.
func (p NodeProfile) change(ctx context.Context, h *stage.Host) (nodeset.Change, error) {
	n, err := findNode(ctx, h, p.Node)
	if err != nil {
		return nodeset.Change{}, err
	}
	if n == nil {
		return nodeset.Change{}, fmt.Errorf("no Node %s", p.Node)
	}
	return nodeset.Diff(n.Labels, p.Site.Labels()), nil
}

func (p NodeProfile) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	c, err := p.change(ctx, h)
	if err != nil {
		return stage.Status{}, err
	}
	if c.Empty() {
		return stage.Status{Done: true, Detail: p.Node + " carries the profile of site " + p.Site.Name}, nil
	}
	return stage.Status{Detail: "not yet: " + c.String()}, nil
}

// Preview is the label command: the whole profile for a Node that does not
// exist yet, else only what differs.
func (p NodeProfile) Preview(ctx context.Context, h *stage.Host) (string, error) {
	c := nodeset.Change{Set: p.Site.Labels()}
	if h.Exec != nil {
		var err error
		if c, err = p.change(ctx, h); err != nil {
			return "", err
		}
	}
	return "kubectl " + strings.Join(kube.LabelArgs(p.Node, c.Set, c.Remove), " ") + "\n", nil
}

func (p NodeProfile) Act(ctx context.Context, h *stage.Host) error {
	c, err := p.change(ctx, h)
	if err != nil || c.Empty() {
		return err
	}
	return kube.Kubectl{Exec: h.Exec}.Label(ctx, p.Node, c.Set, c.Remove)
}
