package stages

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/local"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// byoRunbook is the Workers runbook's section on a machine the operator
// installed.
const byoRunbook = "docs/runbook/workers.md#add-a-bring-your-own-machine"

// clusterCAFile is the cluster's CA on every joined node.
const clusterCAFile = "/etc/kubernetes/pki/ca.crt"

// ByoCheck refuses a machine the operator installed that kluster cannot
// build: its hostname is not the Node name, it does not run the release and
// architecture the Hetzner Workers run, or it is joined to another cluster.
// It only reads.
type ByoCheck struct {
	Node string
	// Release is the Ubuntu release of the Hetzner image (24.04): the
	// packages kluster pins are built for it.
	Release string
	// ClusterCA is the sha256 of the Control Plane's CA. A machine that holds
	// another CA was joined to another cluster, a rehearsal for example.
	ClusterCA string
}

func (ByoCheck) Name() string    { return "byo-check" }
func (ByoCheck) Runbook() string { return byoRunbook }

func (c ByoCheck) checks() []check {
	return []check{
		{"hostname " + c.Node, `[ "$(hostname)" = ` + local.Quote(c.Node) + " ]"},
		{"ubuntu " + c.Release, `. /etc/os-release && [ "$ID" = ubuntu ] && [ "$VERSION_ID" = ` + local.Quote(c.Release) + " ]"},
		{"amd64", `[ "$(dpkg --print-architecture)" = amd64 ]`},
		{"no other cluster", caCheck(clusterCAFile, c.ClusterCA)},
	}
}

// caCheck holds when the file at path is missing or has the sha256 sum.
func caCheck(path, sum string) string {
	return "[ ! -e " + path + " ] || [ \"$(sha256sum < " + path + " | cut -d' ' -f1)\" = " + local.Quote(sum) + " ]"
}

func (c ByoCheck) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	return probe(ctx, h, c.checks())
}

func (c ByoCheck) Preview(context.Context, *stage.Host) (string, error) {
	return "# reads the hostname, the release, the architecture and any cluster CA; changes nothing\n", nil
}

// Act changes nothing: a machine that fails the check needs the operator.
func (c ByoCheck) Act(ctx context.Context, h *stage.Host) error {
	st, err := c.Probe(ctx, h)
	if err != nil || st.Done {
		return err
	}
	return errors.New("kluster cannot build this machine; it lacks " + strings.TrimPrefix(st.Detail, "not yet: ") +
		" (see " + byoRunbook + ")")
}

// NodeRegistered brings back the Node of a joined machine after it was
// deleted. A running kubelet never registers again, and a restart does
// (kubernetes v1.37.0), so the dead-node remover's work is undone here. It
// runs on the machine and reads the Control Plane.
type NodeRegistered struct {
	ControlPlane stage.Exec
	Node         string
	Every        time.Duration
	Timeout      time.Duration
}

func (NodeRegistered) Name() string    { return "node-registered" }
func (NodeRegistered) Runbook() string { return byoRunbook }

func (n NodeRegistered) Probe(ctx context.Context, _ *stage.Host) (stage.Status, error) {
	nodes, err := kube.Kubectl{Exec: n.ControlPlane}.Nodes(ctx)
	if err != nil {
		return stage.Status{}, err
	}
	for _, node := range nodes {
		if node.Name == n.Node {
			return stage.Status{Done: true, Detail: "Node " + n.Node + " exists"}, nil
		}
	}
	return stage.Status{Detail: "no Node " + n.Node + ": the kubelet registers it again at a restart"}, nil
}

func (n NodeRegistered) Preview(context.Context, *stage.Host) (string, error) {
	return fmt.Sprintf("systemctl restart kubelet\n# wait up to %s for Node %s\n", n.Timeout, n.Node), nil
}

func (n NodeRegistered) Act(ctx context.Context, h *stage.Host) error {
	if _, err := h.Exec.Run(ctx, "systemctl restart kubelet"); err != nil {
		return err
	}
	wait, cancel := context.WithTimeout(ctx, n.Timeout)
	defer cancel()
	for {
		st, err := n.Probe(wait, h)
		if err == nil && st.Done {
			return nil
		}
		select {
		case <-wait.Done():
			return fmt.Errorf("Node %s did not register within %s after the kubelet restart", n.Node, n.Timeout)
		case <-time.After(n.Every):
		}
	}
}
