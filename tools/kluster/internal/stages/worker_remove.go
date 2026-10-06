package stages

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/filediff"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/wgconf"
)

// removeRunbook is the Workers runbook's removal section.
const removeRunbook = "docs/runbook/workers.md#remove-a-worker"

// findNode returns the named Node, if the cluster has one.
func findNode(ctx context.Context, h *stage.Host, name string) (*kube.Node, error) {
	nodes, err := kube.Kubectl{Exec: h.Exec}.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		if n.Name == name {
			return &n, nil
		}
	}
	return nil, nil
}

// Drain moves every workload off a Worker's Node, on the Control Plane. The
// drain respects PodDisruptionBudgets, so a Worker that holds the only ready
// instance of a guarded workload (a CNPG primary) blocks until the timeout:
// that is the intended safety.
type Drain struct {
	Node    string
	Timeout time.Duration
}

func (Drain) Name() string    { return "drain" }
func (Drain) Runbook() string { return removeRunbook }

func (d Drain) args() []string {
	return []string{"drain", d.Node, "--ignore-daemonsets", "--delete-emptydir-data", "--timeout=" + d.Timeout.String()}
}

// Probe is done when no Node is left, or the Node is cordoned and holds
// only pods a drain leaves (DaemonSet, static, finished).
func (d Drain) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	n, err := findNode(ctx, h, d.Node)
	if err != nil || n == nil {
		return stage.Status{Done: err == nil, Detail: "no Node " + d.Node}, err
	}
	pods, err := kube.Kubectl{Exec: h.Exec}.PodsOn(ctx, d.Node)
	if err != nil {
		return stage.Status{}, err
	}
	var left []string
	for _, p := range pods {
		if p.Remains() {
			left = append(left, p.Namespace+"/"+p.Name)
		}
	}
	var not []string
	if !n.Unschedulable {
		not = append(not, "cordoned")
	}
	if len(left) > 0 {
		not = append(not, fmt.Sprintf("%d pods moved (%s)", len(left), strings.Join(left, ", ")))
	}
	if len(not) > 0 {
		return stage.Status{Detail: "not yet: " + strings.Join(not, ", ")}, nil
	}
	return stage.Status{Done: true, Detail: "cordoned, only DaemonSet, static or finished pods left"}, nil
}

func (d Drain) Preview(context.Context, *stage.Host) (string, error) {
	return "kubectl " + strings.Join(d.args(), " ") + "\n", nil
}

func (d Drain) Act(ctx context.Context, h *stage.Host) error {
	_, err := kube.Kubectl{Exec: h.Exec}.Run(ctx, d.args()...)
	return err
}

// DeleteNode deletes a drained Worker's Node, on the Control Plane. A Node
// left behind would hand its taints and labels to a later server of the same
// name (infra/workers/main.tf).
type DeleteNode struct {
	Node string
}

func (DeleteNode) Name() string    { return "delete-node" }
func (DeleteNode) Runbook() string { return removeRunbook }

func (d DeleteNode) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	n, err := findNode(ctx, h, d.Node)
	if err != nil {
		return stage.Status{}, err
	}
	if n != nil {
		return stage.Status{Detail: "not yet: Node " + d.Node + " exists"}, nil
	}
	return stage.Status{Done: true, Detail: "no Node " + d.Node}, nil
}

func (d DeleteNode) Preview(context.Context, *stage.Host) (string, error) {
	return "kubectl delete node " + d.Node + "\n", nil
}

// Act refuses a Node that is not cordoned: only a drained Node is deleted,
// whatever order the Stages are put in.
func (d DeleteNode) Act(ctx context.Context, h *stage.Host) error {
	n, err := findNode(ctx, h, d.Node)
	if err != nil || n == nil {
		return err
	}
	if !n.Unschedulable {
		return fmt.Errorf("Node %s is not cordoned: drain it first", d.Node)
	}
	_, err = kube.Kubectl{Exec: h.Exec}.Run(ctx, "delete", "node", d.Node)
	return err
}

// HubPeerRemove removes one Worker from the hub: from the live interface
// with `wg set … remove`, and from the hub config so it stays removed after
// a reboot. The hub is never restarted (as HubPeer). The peer is found by
// its address, never by the name comment: the hand-built live hub may have
// no `# k8swkN` comments. The file holds the hub's private key, so it is
// shown only as a redacted diff.
type HubPeerRemove struct {
	AllowedIPs string // <vpn ip>/32
	Now        func() time.Time
}

func (HubPeerRemove) Name() string    { return "hub-peer-remove" }
func (HubPeerRemove) Runbook() string { return removeRunbook }

func (p HubPeerRemove) read(ctx context.Context, h *stage.Host) (string, error) {
	conf, err := h.Exec.ReadFile(ctx, wgConf)
	if err != nil {
		return "", errors.New("reading the hub config failed") // the error would carry the file
	}
	return conf, nil
}

// filePeer finds the [Peer] of the hub config that routes the address. A
// peer that routes other addresses too is refused: removing it would cut
// them off.
func (p HubPeerRemove) filePeer(conf string) (wgconf.Peer, bool, error) {
	var found []wgconf.Peer
	for _, q := range wgconf.Peers(conf) {
		ips := strings.Split(q.AllowedIPs, ",")
		for _, ip := range ips {
			if strings.TrimSpace(ip) != p.AllowedIPs {
				continue
			}
			if len(ips) > 1 {
				return wgconf.Peer{}, false, fmt.Errorf("the [Peer] that holds %s routes %s: kluster removes only a peer of that one address", p.AllowedIPs, q.AllowedIPs)
			}
			found = append(found, q)
		}
	}
	switch len(found) {
	case 0:
		return wgconf.Peer{}, false, nil
	case 1:
		return found[0], true, nil
	}
	return wgconf.Peer{}, false, fmt.Errorf("%d [Peer] sections hold %s", len(found), p.AllowedIPs)
}

// livePeer finds the key of the live peer that routes the address, from
// `wg show wg0 allowed-ips`: one `<key>\t<ip> <ip>…` line per peer.
func (p HubPeerRemove) livePeer(ctx context.Context, h *stage.Host) (string, bool, error) {
	out, err := h.Exec.Run(ctx, "wg show wg0 allowed-ips")
	if err != nil {
		return "", false, err
	}
	var keys []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(l)
		for _, ip := range f[min(1, len(f)):] {
			if ip == p.AllowedIPs {
				keys = append(keys, f[0])
			}
		}
	}
	switch len(keys) {
	case 0:
		return "", false, nil
	case 1:
		return keys[0], true, nil
	}
	return "", false, fmt.Errorf("%d live peers route %s", len(keys), p.AllowedIPs)
}

// state reads both: the config and the peer it holds, and the live peer.
type hubState struct {
	conf     string
	file     wgconf.Peer
	inFile   bool
	liveKey  string
	liveHeld bool
}

func (p HubPeerRemove) state(ctx context.Context, h *stage.Host) (hubState, error) {
	var s hubState
	var err error
	if s.conf, err = p.read(ctx, h); err != nil {
		return s, err
	}
	if s.file, s.inFile, err = p.filePeer(s.conf); err != nil {
		return s, err
	}
	if s.liveKey, s.liveHeld, err = p.livePeer(ctx, h); err != nil {
		return s, err
	}
	if s.inFile && s.liveHeld && s.file.PublicKey != s.liveKey {
		return s, fmt.Errorf("the hub config and the live hub give %s to different keys: kluster removes neither", p.AllowedIPs)
	}
	return s, nil
}

func (p HubPeerRemove) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	s, err := p.state(ctx, h)
	if err != nil {
		return stage.Status{}, err
	}
	var not []string
	if s.inFile {
		not = append(not, "out of "+wgConf)
	}
	if s.liveHeld {
		not = append(not, "out of the live wg0")
	}
	if len(not) > 0 {
		return stage.Status{Detail: "not yet: " + strings.Join(not, ", ")}, nil
	}
	return stage.Status{Done: true, Detail: "no peer holds " + p.AllowedIPs}, nil
}

// edit returns the hub config now and after the edit.
func (p HubPeerRemove) edit(s hubState) (string, error) {
	if !s.inFile {
		return s.conf, nil
	}
	updated, err := wgconf.RemovePeer(s.conf, s.file.PublicKey)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(updated, "\n") + "\n", nil
}

func (p HubPeerRemove) Preview(ctx context.Context, h *stage.Host) (string, error) {
	if h.Exec == nil {
		return "# back up " + wgConf + ", remove the [Peer] that holds " + p.AllowedIPs + ", then\nwg set wg0 peer <its key> remove\n", nil
	}
	s, err := p.state(ctx, h)
	if err != nil {
		return "", err
	}
	updated, err := p.edit(s)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if s.inFile {
		b.WriteString("# back up " + wgConf + ", remove the [Peer] that holds " + p.AllowedIPs + "\n")
	}
	if s.liveHeld {
		b.WriteString("wg set wg0 peer " + s.liveKey + " remove\n")
	}
	b.WriteString(filediff.Unified(wgConf, s.conf, updated))
	return b.String(), nil
}

// Act writes the file first, then removes the live peer: a run that stops
// between the two finds the live peer by its address and goes on.
func (p HubPeerRemove) Act(ctx context.Context, h *stage.Host) error {
	s, err := p.state(ctx, h)
	if err != nil {
		return err
	}
	if s.inFile {
		updated, err := p.edit(s)
		if err != nil {
			return err
		}
		bak := wgConf + ".bak-kluster-" + p.Now().UTC().Format("20060102T150405Z")
		if _, err := h.Exec.Run(ctx, "cp -p "+wgConf+" "+bak); err != nil {
			return err
		}
		if err := h.Exec.WriteFile(ctx, wgConf, updated, 0o600); err != nil {
			return err
		}
	}
	if s.liveHeld {
		_, err = h.Exec.Run(ctx, "wg set wg0 peer "+s.liveKey+" remove")
	}
	return err
}
