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
// nodeset.Prefix (ADR 0016, Amendments). It also gives a Node of a
// best-effort site the reliability taint, and takes it off a Node of high
// reliability. Labels outside the prefix and other taints belong to someone
// else and stay.
type NodeProfile struct {
	Node string
	Site nodeset.Site
}

func (NodeProfile) Name() string    { return "node-profile" }
func (NodeProfile) Runbook() string { return "docs/runbook/workers.md#set-the-node-profile" }

// profileChange is what a Node still lacks of its site's profile.
type profileChange struct {
	labels nodeset.Change
	taint  nodeset.TaintChange
}

func (c profileChange) empty() bool { return c.labels.Empty() && c.taint.Empty() }

func (c profileChange) String() string {
	var parts []string
	for _, p := range []string{c.labels.String(), c.taint.String()} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "; ")
}

// change is what the Node still lacks. A missing Node is an error: the Stage
// runs only after the Node has joined.
func (p NodeProfile) change(ctx context.Context, h *stage.Host) (profileChange, error) {
	n, err := findNode(ctx, h, p.Node)
	if err != nil {
		return profileChange{}, err
	}
	if n == nil {
		return profileChange{}, fmt.Errorf("no Node %s", p.Node)
	}
	return profileChange{labels: nodeset.Diff(n.Labels, p.Site.Labels()), taint: nodeset.DiffTaint(n.Taints, p.Site)}, nil
}

func (p NodeProfile) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	c, err := p.change(ctx, h)
	if err != nil {
		return stage.Status{}, err
	}
	if c.empty() {
		return stage.Status{Done: true, Detail: p.Node + " carries the profile of site " + p.Site.Name}, nil
	}
	return stage.Status{Detail: "not yet: " + c.String()}, nil
}

// Preview is the label and taint commands: the whole profile for a Node that
// does not exist yet, else only what differs.
func (p NodeProfile) Preview(ctx context.Context, h *stage.Host) (string, error) {
	c := profileChange{labels: nodeset.Change{Set: p.Site.Labels()}, taint: nodeset.DiffTaint(nil, p.Site)}
	if h.Exec != nil {
		var err error
		if c, err = p.change(ctx, h); err != nil {
			return "", err
		}
	}
	var b strings.Builder
	if !c.labels.Empty() {
		b.WriteString("kubectl " + strings.Join(kube.LabelArgs(p.Node, c.labels.Set, c.labels.Remove), " ") + "\n")
	}
	if c.taint.RemoveKey {
		b.WriteString("kubectl taint node " + p.Node + " " + nodeset.ReliabilityTaint.Key + "-\n")
	}
	if c.taint.Add != "" {
		b.WriteString("kubectl taint node " + p.Node + " " + c.taint.Add + " --overwrite\n")
	}
	return b.String(), nil
}

func (p NodeProfile) Act(ctx context.Context, h *stage.Host) error {
	c, err := p.change(ctx, h)
	if err != nil || c.empty() {
		return err
	}
	k := kube.Kubectl{Exec: h.Exec}
	if !c.labels.Empty() {
		if err := k.Label(ctx, p.Node, c.labels.Set, c.labels.Remove); err != nil {
			return err
		}
	}
	if c.taint.RemoveKey {
		if err := k.RemoveTaintKey(ctx, p.Node, nodeset.ReliabilityTaint.Key); err != nil {
			return err
		}
	}
	if c.taint.Add != "" {
		return k.AddTaint(ctx, p.Node, c.taint.Add)
	}
	return nil
}
