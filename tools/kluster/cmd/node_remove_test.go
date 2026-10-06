package cmd

import (
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/intent"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
)

// Run 2 deletes no server whose Node is still in the cluster.
func TestNodeGoneRefusesALiveNode(t *testing.T) {
	nodes := []kube.Node{{Name: "k8s-cp"}, {Name: "k8swk4"}}
	if err := nodeGone(nodes, "k8swk4"); err == nil || !strings.Contains(err.Error(), "kubectl delete node k8swk4") {
		t.Fatalf("got %v", err)
	}
	if err := nodeGone(nodes, "k8swk5"); err != nil {
		t.Fatal(err)
	}
}

// Run 2's plan is one delete of that server: nothing more, nothing less.
func TestDeleteIntent(t *testing.T) {
	in := deleteIntent("k8swk4")
	del := intent.Change{Address: `hcloud_server.workers["k8swk4"]`, Action: intent.Delete}
	for name, c := range map[string]struct {
		changes []intent.Change
		ok      bool
	}{
		"the delete":             {[]intent.Change{del}, true},
		"nothing (not in state)": {nil, false},
		"another Worker too":     {[]intent.Change{del, {Address: `hcloud_server.workers["k8swk3"]`, Action: intent.Delete}}, false},
		"a replace":              {[]intent.Change{{Address: del.Address, Action: intent.Replace}}, false},
	} {
		if mm := in.Check(c.changes); (len(mm) == 0) != c.ok {
			t.Errorf("%s: mismatches %v, want ok %v", name, mm, c.ok)
		}
	}
}

func TestNotReservedVPN(t *testing.T) {
	c := nodeAddConfig()
	for ip, ok := range map[string]bool{"10.100.0.4": true, "10.100.0.1": false, "10.100.0.254": false, "10.100.0.3": false, "x": false} {
		if err := notReservedVPN(c, ip); (err == nil) != ok {
			t.Errorf("%s: err = %v, want ok %v", ip, err, ok)
		}
	}
}
