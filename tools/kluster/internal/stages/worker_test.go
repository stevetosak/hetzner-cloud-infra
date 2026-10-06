package stages

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/wgconf"
)

// fileHost is a scriptHost whose files can be read back.
type fileHost struct {
	scriptHost
	files map[string]string
}

func (f *fileHost) ReadFile(_ context.Context, path string) (string, error) {
	return f.files[path], nil
}

func (f *fileHost) WriteFile(ctx context.Context, path, content string, mode os.FileMode) error {
	f.ran = append(f.ran, "write "+path)
	return f.scriptHost.WriteFile(ctx, path, content, mode)
}

const hubConf = `[Interface]
Address    = 10.100.0.1/24
ListenPort = 51820
PrivateKey = HUB-PRIVATE-KEY

[Peer]
# admin-laptop
PublicKey  = laptopkey=
AllowedIPs = 10.100.0.69/32`

func newHubPeer() HubPeer {
	return HubPeer{
		Peer: wgconf.Peer{Name: "k8swk4", PublicKey: "workerkey=", AllowedIPs: "10.100.0.5/32"},
		Now:  func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) },
	}
}

func TestHubPeerAppendsBacksUpAndSetsLive(t *testing.T) {
	fh := &fileHost{files: map[string]string{wgConf: hubConf}}
	if err := newHubPeer().Act(context.Background(), &stage.Host{Exec: fh}); err != nil {
		t.Fatal(err)
	}
	got := fh.written[wgConf]
	if !strings.HasPrefix(got, hubConf+"\n\n[Peer]\n# k8swk4\nPublicKey  = workerkey=\nAllowedIPs = 10.100.0.5/32\n") {
		t.Fatalf("the hub config was not appended to:\n%s", got)
	}
	want := []string{"cp -p " + wgConf + " " + wgConf + ".bak-kluster-20261006T120000Z", "write " + wgConf, "wg set wg0 peer workerkey= allowed-ips 10.100.0.5/32"}
	if strings.Join(fh.ran, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ran %q, want %q", fh.ran, want)
	}
	for _, c := range fh.ran {
		if strings.Contains(c, "restart") {
			t.Fatalf("the hub's wg-quick was restarted: %s", c)
		}
	}
}

// A run that stopped after the write and before `wg set` writes nothing again.
func TestHubPeerResumesWithoutWritingTwice(t *testing.T) {
	once, err := wgconf.AddPeer(hubConf, newHubPeer().Peer)
	if err != nil {
		t.Fatal(err)
	}
	fh := &fileHost{files: map[string]string{wgConf: once}}
	if err := newHubPeer().Act(context.Background(), &stage.Host{Exec: fh}); err != nil {
		t.Fatal(err)
	}
	if len(fh.written) != 0 || len(fh.ran) != 1 || !strings.HasPrefix(fh.ran[0], "wg set wg0 peer workerkey=") {
		t.Fatalf("ran %q, wrote %v", fh.ran, fh.written)
	}
}

func TestHubPeerPreviewHidesTheHubKey(t *testing.T) {
	fh := &fileHost{files: map[string]string{wgConf: hubConf}}
	p, err := newHubPeer().Preview(context.Background(), &stage.Host{Exec: fh})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p, "HUB-PRIVATE-KEY") || !strings.Contains(p, "+PublicKey  = workerkey=") {
		t.Fatalf("preview:\n%s", p)
	}
}

func TestJoinKeepsTheTokenOffCommandLines(t *testing.T) {
	const token = "abcdef.0123456789abcdef"
	cp := &scriptHost{out: map[string]string{mintJoin: "kubeadm join k8s-cp.tosak.internal:6443 --token " + token + "\nreboot"}}
	wk := &scriptHost{}
	// A second line would run something else after the join.
	if err := (Join{ControlPlane: cp, Endpoint: "k8s-cp.tosak.internal"}).Act(context.Background(), &stage.Host{Exec: wk}); err == nil {
		t.Fatal("a join command with a second line was accepted")
	}
	cp.out[mintJoin] = "kubeadm join k8s-cp.tosak.internal:6443 --token " + token + " --discovery-token-ca-cert-hash sha256:aa"
	if err := (Join{ControlPlane: cp, Endpoint: "k8s-cp.tosak.internal"}).Act(context.Background(), &stage.Host{Exec: wk}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wk.written[joinFile], token) {
		t.Fatalf("the join command was not written to %s", joinFile)
	}
	for _, c := range append(cp.ran, wk.ran...) {
		if strings.Contains(c, token) {
			t.Fatalf("the token is on a command line: %s", c)
		}
	}
	if !strings.Contains(Join{}.run(), "trap 'rm -f "+joinFile+"' EXIT") {
		t.Fatal("the join file is not removed")
	}
	cp.out[mintJoin] = "kubeadm join 10.0.1.5:6443 --token " + token
	if err := (Join{ControlPlane: cp, Endpoint: "k8s-cp.tosak.internal"}).Act(context.Background(), &stage.Host{Exec: &scriptHost{}}); err == nil {
		t.Fatal("a join command for another endpoint was accepted")
	}
}

func TestWorkerKubePrepHoldsCNIAndResolvesTheControlPlane(t *testing.T) {
	k := KubePrep{Minor: "v1.37", Endpoint: "k8s-cp.tosak.internal", EndpointIP: "10.0.1.5", PrivateIP: "10.0.2.9", HoldCNI: true}
	s := k.script()
	for _, want := range []string{"apt-mark hold kubelet kubeadm kubectl kubernetes-cni\n", "'10.0.1.5  k8s-cp.tosak.internal'", "--node-ip=10.0.2.9 "} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	var held string
	for _, c := range k.checks() {
		if c.name == "held" {
			held = c.cmd
		}
	}
	if !strings.Contains(held, "'kubelet|kubeadm|kubectl|kubernetes-cni')\" = 4") {
		t.Fatalf("held check: %s", held)
	}
}

func TestBaseHostWithoutCNIPlugins(t *testing.T) {
	b := BaseHost{User: "tosak", Containerd: "2.2.0", Runc: "1.4.0"}
	if strings.Contains(b.script(), "cni-plugins") {
		t.Fatal("the Worker script still downloads the CNI plugin tarball")
	}
	for _, c := range b.checks() {
		if c.name == "cni-plugins" {
			t.Fatal("the Worker probe still checks the CNI plugin tarball")
		}
	}
	b.CNIPlugins = "1.9.0"
	if !strings.Contains(b.script(), "cni-plugins-linux-amd64-v1.9.0.tgz") {
		t.Fatal("the Control Plane script lost the CNI plugin tarball")
	}
}

func TestNodeReadyStatus(t *testing.T) {
	n := NodeReady{Node: "k8swk4", PrivateIP: "10.0.2.9", ServerID: "42"}
	good := kube.Node{Name: "k8swk4", Ready: true, InternalIP: "10.0.2.9", ProviderID: "hcloud://42"}
	if st := n.status(good); !st.Done {
		t.Fatalf("good Node not done: %+v", st)
	}
	tainted := good
	tainted.Uninitialized = true
	if st := n.status(tainted); st.Done || st.Detail != "not yet: initialized" {
		t.Fatalf("tainted Node: %+v", st)
	}
	public := good
	public.InternalIP = "203.0.113.9"
	if st := n.status(public); st.Done {
		t.Fatal("a Node on its public address is done")
	}
}

func TestAtNamesAnotherRunbook(t *testing.T) {
	s := At(PrivateNetwork{}, "docs/runbook/workers.md#3-base-host-setup")
	if s.Runbook() != "docs/runbook/workers.md#3-base-host-setup" || s.Name() != "private-network" {
		t.Fatalf("got %s %s", s.Name(), s.Runbook())
	}
}
