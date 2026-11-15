package cmd

import (
	"reflect"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

func stageNames(steps []stage.Stage) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.Name()
	}
	return out
}

// The room check runs before anything changes, the ConfigMap before the
// file kubeadm writes from it, the file before the restart, and the proof
// last.
func TestKubeletStagesRunInOrder(t *testing.T) {
	s := kubeletStages("k8swk2", map[string]any{"kubeReserved": map[string]string{"memory": "160Mi"}}, nil)
	got := [][]string{stageNames(s.before), stageNames(s.node), stageNames(s.proof)}
	want := [][]string{{"kubelet-room", "kubelet-configmap"}, {"kubelet-config-file", "kubelet-restart"}, {"kubelet-proof"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
