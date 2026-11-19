package cmd

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stages"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

func flannelNodes(s podMTUSteps) []string {
	var out []string
	for _, n := range s.flannel {
		out = append(out, n.node+":"+strings.Join(stageNames(n.steps), ","))
	}
	return out
}

// The ConfigMap goes first, then flannel.1 on each Node in order, and the
// restarts and the proof last, after every Node has its new tunnel.
func TestPodMTUStagesRunInOrder(t *testing.T) {
	s := podMTUStages("", 1350, []string{"k8s-cp", "k8swk2"}, nil, nil, "")
	got := [][]string{stageNames(s.config), flannelNodes(s), stageNames(s.restart)}
	want := [][]string{{"flannel-config"}, {"k8s-cp:flannel-mtu", "k8swk2:flannel-mtu"}, {"pod-mtu-restart", "pod-mtu-proof"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, n := range s.flannel {
		if m := n.steps[0].(stages.FlannelMTU); m.Node != n.node || m.PodMTU != 1350 || m.Wait != flannelWait {
			t.Errorf("%s: %+v", n.node, m)
		}
	}
	if r := s.restart[0].(stages.PodMTURestart); r.Wait != rolloutWait {
		t.Errorf("restart waits %s", r.Wait)
	}
	if p := s.restart[1].(stages.PodMTUProof); p.Wait != proofWait {
		t.Errorf("proof waits %s", p.Wait)
	}
}

// --until flannel leaves the pods at their old MTU: the mixed state.
func TestPodMTUStagesStopAfterFlannel(t *testing.T) {
	s := podMTUStages("", 1350, []string{"k8s-cp"}, nil, map[string]stage.Exec{}, untilFlannel)
	if len(s.restart) != 0 || len(s.flannel) != 1 || len(s.config) != 1 {
		t.Fatalf("got %+v", s)
	}
}

func TestLoginsForRefusesANodeOutsideTheWorkerSet(t *testing.T) {
	set, err := workerset.Parse("t.tfvars", []byte(`workers = {
  k8swk2 = { private_ip = "10.0.2.6", vpn_ip = "10.100.0.2", server_type = "cx23", labels = {} }
}
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := loginsFor([]string{"k8s-cp", "k8swk2"}, "k8s-cp", set, "t.tfvars"); err != nil {
		t.Fatal(err)
	}
	err = loginsFor([]string{"k8s-cp", "k8swk2", "ts1-a"}, "k8s-cp", set, "t.tfvars")
	if err == nil || !strings.Contains(err.Error(), "the Node ts1-a is not the Control Plane and not in the Worker set t.tfvars") {
		t.Fatalf("got %v", err)
	}
}
