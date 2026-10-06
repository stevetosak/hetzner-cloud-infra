package stages

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/wgconf"
)

const (
	getNodes = "KUBECONFIG=/etc/kubernetes/admin.conf kubectl 'get' 'nodes'"
	getPods  = "KUBECONFIG=/etc/kubernetes/admin.conf kubectl 'get' 'pods'"
	wgShow   = "wg show wg0 allowed-ips"
)

func nodesJSON(unschedulable bool) string {
	u := "false"
	if unschedulable {
		u = "true"
	}
	return `{"items":[{"metadata":{"name":"k8s-cp"}},{"metadata":{"name":"k8swk4"},"spec":{"unschedulable":` + u + `}}]}`
}

const podsLeft = `{"items":[
 {"metadata":{"namespace":"kube-flannel","name":"flannel-x","ownerReferences":[{"kind":"DaemonSet"}]},"status":{"phase":"Running"}},
 {"metadata":{"namespace":"doma","name":"web-x","ownerReferences":[{"kind":"ReplicaSet"}]},"status":{"phase":"Running"}}]}`

const onlyDaemonSets = `{"items":[
 {"metadata":{"namespace":"kube-flannel","name":"flannel-x","ownerReferences":[{"kind":"DaemonSet"}]},"status":{"phase":"Running"}}]}`

func TestDrainProbe(t *testing.T) {
	d := Drain{Node: "k8swk4", Timeout: 5 * time.Minute}
	for _, c := range []struct {
		nodes, pods string
		done        bool
		detail      string
	}{
		{`{"items":[{"metadata":{"name":"k8s-cp"}}]}`, "", true, "no Node k8swk4"},
		{nodesJSON(false), onlyDaemonSets, false, "not yet: cordoned"},
		{nodesJSON(true), podsLeft, false, "not yet: 1 pods moved (doma/web-x)"},
		{nodesJSON(true), onlyDaemonSets, true, ""},
	} {
		h := &stage.Host{Exec: &scriptHost{out: map[string]string{getNodes: c.nodes, getPods: c.pods}}}
		st, err := d.Probe(context.Background(), h)
		if err != nil {
			t.Fatal(err)
		}
		if st.Done != c.done || (c.detail != "" && st.Detail != c.detail) {
			t.Errorf("got %+v, want done %v %q", st, c.done, c.detail)
		}
	}
	if p, _ := d.Preview(context.Background(), &stage.Host{}); p != "kubectl drain k8swk4 --ignore-daemonsets --delete-emptydir-data --timeout=5m0s\n" {
		t.Errorf("preview %q", p)
	}
}

func TestDeleteNodeRefusesAnUndrainedNode(t *testing.T) {
	sh := &scriptHost{out: map[string]string{getNodes: nodesJSON(false)}}
	if err := (DeleteNode{Node: "k8swk4"}).Act(context.Background(), &stage.Host{Exec: sh}); err == nil {
		t.Fatal("an uncordoned Node was deleted")
	}
	sh = &scriptHost{out: map[string]string{getNodes: nodesJSON(true)}}
	if err := (DeleteNode{Node: "k8swk4"}).Act(context.Background(), &stage.Host{Exec: sh}); err != nil {
		t.Fatal(err)
	}
	if last := sh.ran[len(sh.ran)-1]; last != "KUBECONFIG=/etc/kubernetes/admin.conf kubectl 'delete' 'node' 'k8swk4'" {
		t.Fatalf("ran %q", last)
	}
}

// The live hub may have no name comments, so the peer is found by address.
const hubWithWorkers = hubConf + `

[Peer]
PublicKey  = wk3key=
AllowedIPs = 10.100.0.4/32

[Peer]
PublicKey  = wk4key=
AllowedIPs = 10.100.0.5/32

[Peer]
PublicKey  = wk5key=
AllowedIPs = 10.100.0.6/32
`

const liveWithWorkers = "laptopkey=\t10.100.0.69/32\nwk3key=\t10.100.0.4/32\nwk4key=\t10.100.0.5/32\nwk5key=\t10.100.0.6/32\n"

func newHubPeerRemove() HubPeerRemove {
	return HubPeerRemove{AllowedIPs: "10.100.0.5/32",
		Now: func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) }}
}

func TestHubPeerRemoveByAddressBacksUpAndRemovesLive(t *testing.T) {
	fh := &fileHost{files: map[string]string{wgConf: hubWithWorkers}}
	fh.out = map[string]string{wgShow: liveWithWorkers}
	if err := newHubPeerRemove().Act(context.Background(), &stage.Host{Exec: fh}); err != nil {
		t.Fatal(err)
	}
	want, err := wgconf.RemovePeer(hubWithWorkers, "wk4key=")
	if err != nil {
		t.Fatal(err)
	}
	if got := fh.written[wgConf]; got != want || strings.Contains(got, "wk4key=") || !strings.Contains(got, "wk5key=") {
		t.Fatalf("hub config:\n%s", got)
	}
	ran := strings.Join(fh.ran, "\n")
	for _, w := range []string{"cp -p " + wgConf + " " + wgConf + ".bak-kluster-20261006T120000Z", "write " + wgConf, "wg set wg0 peer wk4key= remove"} {
		if !strings.Contains(ran, w) {
			t.Errorf("did not run %q in:\n%s", w, ran)
		}
	}
	if strings.Contains(ran, "restart") {
		t.Fatal("the hub's wg-quick was restarted")
	}
}

// A run that stopped after the write removes only the live peer.
func TestHubPeerRemoveResumesAfterTheWrite(t *testing.T) {
	once, err := wgconf.RemovePeer(hubWithWorkers, "wk4key=")
	if err != nil {
		t.Fatal(err)
	}
	fh := &fileHost{files: map[string]string{wgConf: once}}
	fh.out = map[string]string{wgShow: liveWithWorkers}
	h := &stage.Host{Exec: fh}
	if st, _ := newHubPeerRemove().Probe(context.Background(), h); st.Done || st.Detail != "not yet: out of the live wg0" {
		t.Fatalf("probe %+v", st)
	}
	if err := newHubPeerRemove().Act(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if len(fh.written) != 0 || fh.ran[len(fh.ran)-1] != "wg set wg0 peer wk4key= remove" {
		t.Fatalf("ran %q, wrote %v", fh.ran, fh.written)
	}
	fh.out[wgShow] = "laptopkey=\t10.100.0.69/32\n"
	if st, _ := newHubPeerRemove().Probe(context.Background(), h); !st.Done {
		t.Fatalf("probe after removal %+v", st)
	}
}

func TestHubPeerRemoveRefuses(t *testing.T) {
	for name, c := range map[string]struct{ conf, live string }{
		"a peer that routes more": {hubConf + "\n\n[Peer]\nPublicKey = wk4key=\nAllowedIPs = 10.100.0.5/32, 10.100.0.7/32\n", ""},
		"two peers":               {hubWithWorkers + "\n[Peer]\nPublicKey = other=\nAllowedIPs = 10.100.0.5/32\n", ""},
		"file and live disagree":  {hubWithWorkers, "otherkey=\t10.100.0.5/32\n"},
	} {
		fh := &fileHost{files: map[string]string{wgConf: c.conf}}
		fh.out = map[string]string{wgShow: c.live}
		if err := newHubPeerRemove().Act(context.Background(), &stage.Host{Exec: fh}); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if len(fh.written) != 0 {
			t.Errorf("%s: wrote %v", name, fh.written)
		}
	}
}

func TestHubPeerRemovePreviewHidesTheHubKey(t *testing.T) {
	fh := &fileHost{files: map[string]string{wgConf: hubWithWorkers}}
	fh.out = map[string]string{wgShow: liveWithWorkers}
	p, err := newHubPeerRemove().Preview(context.Background(), &stage.Host{Exec: fh})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p, "HUB-PRIVATE-KEY") || !strings.Contains(p, "-PublicKey  = wk4key=") || !strings.Contains(p, "wg set wg0 peer wk4key= remove") {
		t.Fatalf("preview:\n%s", p)
	}
	if len(fh.written) != 0 {
		t.Fatal("the preview wrote")
	}
}
