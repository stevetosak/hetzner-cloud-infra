package stages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/filediff"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// podMTURunbook is the Workers runbook's section on a change of the pod MTU.
const podMTURunbook = "docs/runbook/workers.md#change-the-pod-mtu"

// vxlanOverhead is what Flannel subtracts from Backend.MTU, the underlay MTU,
// to size flannel.1 and every pod interface.
const vxlanOverhead = 50

// The Flannel objects and files a pod MTU change reads.
const (
	flannelNamespace = "kube-flannel"
	flannelConfigMap = "kube-flannel-cfg"
	flannelNetConf   = "net-conf.json"
	flannelPodLabel  = "app=flannel"
	flannelSubnetEnv = "/run/flannel/subnet.env"
)

// The pod MTU Stages of `kluster node pod-mtu` run in this order:
// FlannelConfig on the Control Plane, FlannelMTU on each node, one at a
// time, then the restarts and the proof on the Control Plane. Each takes
// PodMTU, kluster.yaml's cluster.podMTU.

// underlayMTU is the Backend.MTU that gives pods podMTU.
func underlayMTU(podMTU int) int { return podMTU + vxlanOverhead }

// backendMTU reads Backend.MTU from a Flannel net-conf.json.
func backendMTU(netConf string) (int, error) {
	var c struct {
		Backend struct {
			MTU int `json:"MTU"`
		} `json:"Backend"`
	}
	if err := json.Unmarshal([]byte(netConf), &c); err != nil {
		return 0, fmt.Errorf("reading %s: %w", flannelNetConf, err)
	}
	if c.Backend.MTU == 0 {
		return 0, fmt.Errorf("%s sets no Backend.MTU", flannelNetConf)
	}
	return c.Backend.MTU, nil
}

// manifestNetConf finds the net-conf.json of the kube-flannel-cfg ConfigMap
// in the multi-document Flannel manifest.
func manifestNetConf(manifest string) (string, error) {
	dec := yaml.NewDecoder(strings.NewReader(manifest))
	for {
		var doc struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
			Data map[string]string `yaml:"data"`
		}
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("the Flannel manifest has no ConfigMap %s/%s", flannelNamespace, flannelConfigMap)
		}
		if err != nil {
			return "", fmt.Errorf("reading the Flannel manifest: %w", err)
		}
		if doc.Kind == "ConfigMap" && doc.Metadata.Name == flannelConfigMap && doc.Metadata.Namespace == flannelNamespace {
			v, ok := doc.Data[flannelNetConf]
			if !ok {
				return "", fmt.Errorf("ConfigMap %s/%s in the Flannel manifest has no %s", flannelNamespace, flannelConfigMap, flannelNetConf)
			}
			return v, nil
		}
	}
}

// liveBackendMTU reads Backend.MTU from the cluster's Flannel ConfigMap.
func liveBackendMTU(ctx context.Context, cp stage.Exec) (int, string, error) {
	doc, err := kube.Kubectl{Exec: cp}.ConfigMapData(ctx, flannelNamespace, flannelConfigMap, flannelNetConf)
	if err != nil {
		return 0, "", err
	}
	mtu, err := backendMTU(doc)
	return mtu, doc, err
}

// FlannelConfig sets net-conf.json in the cluster's Flannel ConfigMap to the
// one in core/cni/flannel.yaml, on the Control Plane. Only that key changes,
// and the DaemonSet does not, so no Flannel pod restarts: a running Flannel
// reads the value only at its start (FlannelMTU). Act refuses a manifest
// whose Backend.MTU does not give PodMTU.
type FlannelConfig struct {
	Manifest string // core/cni/flannel.yaml
	PodMTU   int
}

func (FlannelConfig) Name() string    { return "flannel-config" }
func (FlannelConfig) Runbook() string { return podMTURunbook }

const flannelCMPath = flannelNamespace + "/" + flannelConfigMap

func (f FlannelConfig) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	got, _, err := liveBackendMTU(ctx, h.Exec)
	if err != nil {
		return stage.Status{}, err
	}
	want := underlayMTU(f.PodMTU)
	if got != want {
		return stage.Status{Detail: fmt.Sprintf("not yet: ConfigMap %s has Backend.MTU %d, pod MTU %d needs %d", flannelCMPath, got, f.PodMTU, want)}, nil
	}
	return stage.Status{Done: true, Detail: fmt.Sprintf("ConfigMap %s has Backend.MTU %d", flannelCMPath, got)}, nil
}

// wanted is the manifest's net-conf.json, refused when it does not give
// PodMTU: kluster.yaml and core/cni/flannel.yaml must agree.
func (f FlannelConfig) wanted() (string, error) {
	doc, err := manifestNetConf(f.Manifest)
	if err != nil {
		return "", err
	}
	mtu, err := backendMTU(doc)
	if err != nil {
		return "", err
	}
	if want := underlayMTU(f.PodMTU); mtu != want {
		return "", fmt.Errorf("core/cni/flannel.yaml has Backend.MTU %d, but kluster.yaml's podMTU %d needs %d", mtu, f.PodMTU, want)
	}
	return doc, nil
}

func (f FlannelConfig) Preview(ctx context.Context, h *stage.Host) (string, error) {
	doc, err := f.wanted()
	if err != nil {
		return "", err
	}
	cmd := "kubectl -n " + flannelNamespace + " patch configmap " + flannelConfigMap + " --type merge (data." + flannelNetConf +
		" from core/cni/flannel.yaml)\n"
	if h.Exec == nil {
		return cmd, nil
	}
	_, live, err := liveBackendMTU(ctx, h.Exec)
	if err != nil {
		return "", err
	}
	return cmd + filediff.Unified(flannelCMPath+"/"+flannelNetConf, live, doc), nil
}

func (f FlannelConfig) Act(ctx context.Context, h *stage.Host) error {
	doc, err := f.wanted()
	if err != nil {
		return err
	}
	return kube.Kubectl{Exec: h.Exec}.PatchConfigMapData(ctx, flannelNamespace, flannelConfigMap, flannelNetConf, doc)
}

// FlannelMTU rebuilds one node's flannel.1 at PodMTU, on that node. Flannel
// does not resize an existing VXLAN device (docs/runbook/control-plane.md,
// "Correction — the MTU was set twice"), and a running flanneld makes a
// deleted flannel.1 again at once, at the MTU it started with, but without
// its routes, FDB entries or neighbours (Flannel's vxlan device watcher). So
// Act first deletes the node's Flannel pod through the Control Plane (CP):
// the new pod reads the ConfigMap and writes FLANNEL_MTU. Then it deletes
// flannel.1, which the new pod makes again at PodMTU, and deletes the pod
// once more, so that a fresh flanneld programs the peers on the new device.
// Last, cni0 gets PodMTU, because the bridge keeps the MTU it was made with.
// The pods keep their own MTU until they restart (PodMTURestart). While the
// device has no routes, pod traffic of this node to other nodes stops.
type FlannelMTU struct {
	Node   string
	PodMTU int
	CP     stage.Exec
	Wait   time.Duration
	Every  time.Duration
}

func (FlannelMTU) Name() string    { return "flannel-mtu" }
func (FlannelMTU) Runbook() string { return podMTURunbook }

// deviceCheck holds once flannel.1 has PodMTU.
func (m FlannelMTU) deviceCheck() check {
	return check{"flannel.1", `[ "$(cat /sys/class/net/flannel.1/mtu)" = ` + strconv.Itoa(m.PodMTU) + " ]"}
}

// subnetEnvCheck holds once a flanneld that read PodMTU's ConfigMap started.
func (m FlannelMTU) subnetEnvCheck() check {
	return check{"subnet.env", "grep -qx 'FLANNEL_MTU=" + strconv.Itoa(m.PodMTU) + "' " + flannelSubnetEnv}
}

// routesCheck holds once flanneld has programmed every other node: it adds a
// route to each one's pod subnet at its start.
func routesCheck(peers int) check {
	return check{"routes", `[ "$(ip -4 route show dev flannel.1 | wc -l)" -ge ` + strconv.Itoa(peers) + " ]"}
}

// tunnelChecks hold once the new Flannel pod has rebuilt flannel.1.
func (m FlannelMTU) tunnelChecks(peers int) []check {
	return []check{m.deviceCheck(), m.subnetEnvCheck(), routesCheck(peers)}
}

func (m FlannelMTU) checks(peers int) []check {
	return append(m.tunnelChecks(peers),
		check{"cni0", `[ ! -e /sys/class/net/cni0 ] || [ "$(cat /sys/class/net/cni0/mtu)" = ` + strconv.Itoa(m.PodMTU) + " ]"})
}

// peers is the number of other nodes, each of which needs a route through
// flannel.1.
func (m FlannelMTU) peers(ctx context.Context) (int, error) {
	nodes, err := kube.Kubectl{Exec: m.CP}.Nodes(ctx)
	if err != nil {
		return 0, err
	}
	return max(len(nodes)-1, 0), nil
}

func (m FlannelMTU) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	n, err := m.peers(ctx)
	if err != nil {
		return stage.Status{}, err
	}
	return probe(ctx, h, m.checks(n))
}

// deletePod is the Control Plane's command that deletes this node's Flannel
// pod; the DaemonSet makes a new one.
func (m FlannelMTU) deletePod() []string {
	return []string{"-n", flannelNamespace, "delete", "pod", "-l", flannelPodLabel,
		"--field-selector", "spec.nodeName=" + m.Node, "--timeout=60s"}
}

func (m FlannelMTU) cni0() string {
	return "if [ -e /sys/class/net/cni0 ]; then ip link set cni0 mtu " + strconv.Itoa(m.PodMTU) + "; fi"
}

func (m FlannelMTU) Preview(context.Context, *stage.Host) (string, error) {
	mtu := strconv.Itoa(m.PodMTU)
	del := "# on the Control Plane: kubectl " + strings.Join(m.deletePod(), " ") + "\n"
	return del +
		"# wait until the new Flannel pod has written FLANNEL_MTU=" + mtu + "\n" +
		"ip link delete flannel.1\n" +
		"# wait until the new Flannel pod has made flannel.1 again at " + mtu + "\n" +
		del +
		"# wait until the next Flannel pod has programmed the routes on flannel.1\n" +
		m.cni0() + "\n", nil
}

// Act refuses while the ConfigMap does not give PodMTU: the new pod would
// build flannel.1 at the old MTU, after an outage for nothing. Each wait that
// runs out stops Act with the checks that still fail.
func (m FlannelMTU) Act(ctx context.Context, h *stage.Host) error {
	got, _, err := liveBackendMTU(ctx, m.CP)
	if err != nil {
		return err
	}
	if want := underlayMTU(m.PodMTU); got != want {
		return fmt.Errorf("ConfigMap %s has Backend.MTU %d, not %d: run flannel-config first", flannelCMPath, got, want)
	}
	peers, err := m.peers(ctx)
	if err != nil {
		return err
	}
	cp := kube.Kubectl{Exec: m.CP}
	if _, err := cp.Run(ctx, m.deletePod()...); err != nil {
		return err
	}
	if err := m.await(ctx, h, "the new Flannel pod's FLANNEL_MTU", []check{m.subnetEnvCheck()}); err != nil {
		return err
	}
	if _, err := h.Exec.Run(ctx, "if [ -e /sys/class/net/flannel.1 ]; then ip link delete flannel.1; fi"); err != nil {
		return err
	}
	if err := m.await(ctx, h, "flannel.1 made again", []check{m.deviceCheck()}); err != nil {
		return err
	}
	if _, err := cp.Run(ctx, m.deletePod()...); err != nil {
		return err
	}
	if err := m.await(ctx, h, "the routes on flannel.1", m.tunnelChecks(peers)); err != nil {
		return err
	}
	_, err = h.Exec.Run(ctx, m.cni0())
	return err
}

// await waits until checks hold on the node, or fails with those that do not.
func (m FlannelMTU) await(ctx context.Context, h *stage.Host, what string, checks []check) error {
	var last stage.Status
	err := waitFor(ctx, m.Wait, m.Every, func() (stage.Status, error) {
		st, err := probe(ctx, h, checks)
		last = st
		return st, err
	})
	if err != nil {
		return err
	}
	if !last.Done {
		return fmt.Errorf("%s: waited %s for %s, %s", m.Node, m.Wait, what, last.Detail)
	}
	return nil
}
