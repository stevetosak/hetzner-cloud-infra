package stages

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// joinFile holds the join command for the one run. It is on /run, a tmpfs,
// and the command that runs it removes it: the token is never on a command
// line, in a kept script or on the terminal.
const (
	joinFile = "/run/kluster/join"
	joinLog  = "/root/kluster-kubeadm-join.log"
)

// Join joins a Worker to the cluster with a token minted for it on the
// Control Plane, valid 15 minutes.
type Join struct {
	ControlPlane stage.Exec
	Endpoint     string // k8s-cp.tosak.internal
}

func (Join) Name() string    { return "join" }
func (Join) Runbook() string { return "docs/runbook/workers.md#7-join-the-cluster" }

const mintJoin = "kubeadm token create --print-join-command --ttl 15m"

func (j Join) run() string {
	return `trap 'rm -f ` + joinFile + `' EXIT
umask 077
if ! bash ` + joinFile + ` > ` + joinLog + ` 2>&1; then
  ` + noToken(joinLog) + `
  exit 1
fi
rm -f ` + joinLog + `
`
}

func (j Join) checks() []check {
	return []check{
		{"kubelet-conf", "[ -s /etc/kubernetes/kubelet.conf ]"},
		{"kubelet", "systemctl is-active --quiet kubelet"},
	}
}

func (j Join) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	return probe(ctx, h, j.checks())
}

func (j Join) Preview(context.Context, *stage.Host) (string, error) {
	return "# on the Control Plane: " + mintJoin + "\n# write it to " + joinFile + " (tmpfs, 0700), then\n" + j.run(), nil
}

func (j Join) Act(ctx context.Context, h *stage.Host) error {
	cmd, err := j.ControlPlane.Run(ctx, mintJoin)
	if err != nil {
		return errors.New("minting a join token on the Control Plane failed") // the error would carry the token
	}
	cmd = strings.TrimSpace(cmd)
	if want := "kubeadm join " + j.Endpoint + ":6443 "; !strings.HasPrefix(cmd, want) || strings.Contains(cmd, "\n") {
		return fmt.Errorf("the minted join command does not start %q", want)
	}
	if _, err := h.Exec.Run(ctx, "mkdir -p -m 700 /run/kluster"); err != nil {
		return err
	}
	if err := h.Exec.WriteFile(ctx, joinFile, cmd+"\n", 0o700); err != nil {
		return err
	}
	_, err = h.Exec.Run(ctx, j.run())
	return err
}

// NodeReady waits on the Control Plane until the joined Worker's Node is
// Ready with its private InternalIP, and the cloud controller has set its
// providerID and cleared the uninitialized taint (docs/runbook/workers.md,
// step 8).
type NodeReady struct {
	Node      string
	PrivateIP string
	ServerID  string
	Every     time.Duration
	Timeout   time.Duration
}

func (NodeReady) Name() string    { return "node-ready" }
func (NodeReady) Runbook() string { return "docs/runbook/workers.md#8-verify-the-cluster" }

func (n NodeReady) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	nodes, err := kube.Kubectl{Exec: h.Exec}.Nodes(ctx)
	if err != nil {
		return stage.Status{}, err
	}
	for _, node := range nodes {
		if node.Name == n.Node {
			return n.status(node), nil
		}
	}
	return stage.Status{Detail: "no Node " + n.Node}, nil
}

func (n NodeReady) status(node kube.Node) stage.Status {
	var not []string
	if !node.Ready {
		not = append(not, "Ready")
	}
	if node.InternalIP != n.PrivateIP {
		not = append(not, "InternalIP "+n.PrivateIP+" (is "+node.InternalIP+")")
	}
	if node.ProviderID != "hcloud://"+n.ServerID {
		not = append(not, "providerID hcloud://"+n.ServerID+" (is "+node.ProviderID+")")
	}
	if node.Uninitialized {
		not = append(not, "initialized")
	}
	if len(not) > 0 {
		return stage.Status{Detail: "not yet: " + strings.Join(not, ", ")}
	}
	return stage.Status{Done: true, Detail: fmt.Sprintf("Ready, %s, hcloud://%s", n.PrivateIP, n.ServerID)}
}

func (n NodeReady) Preview(context.Context, *stage.Host) (string, error) {
	return fmt.Sprintf("# wait up to %s for Node %s: Ready, InternalIP %s, providerID hcloud://%s, no uninitialized taint\n",
		n.Timeout, n.Node, n.PrivateIP, n.ServerID), nil
}

func (n NodeReady) Act(ctx context.Context, h *stage.Host) error {
	wait, cancel := context.WithTimeout(ctx, n.Timeout)
	defer cancel()
	for {
		st, err := n.Probe(wait, h)
		if err == nil && st.Done {
			return nil
		}
		last := st.Detail
		if err != nil {
			last = err.Error()
		}
		select {
		case <-wait.Done():
			return fmt.Errorf("Node %s after %s: %s", n.Node, n.Timeout, last)
		case <-time.After(n.Every):
		}
	}
}
