package stages

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// scriptHost answers every command with out and records what ran.
type scriptHost struct {
	out     map[string]string // command prefix → output
	ran     []string
	written map[string]string
}

func (s *scriptHost) Run(_ context.Context, cmd string) (string, error) {
	s.ran = append(s.ran, cmd)
	for p, o := range s.out {
		if strings.HasPrefix(cmd, p) {
			return o, nil
		}
	}
	return "", nil
}

func (s *scriptHost) WriteFile(_ context.Context, path, content string, _ os.FileMode) error {
	if s.written == nil {
		s.written = map[string]string{}
	}
	s.written[path] = content
	return nil
}

func (s *scriptHost) ReadFile(context.Context, string) (string, error) { return "", nil }

func TestProbeReportsFailedChecksByName(t *testing.T) {
	h := &stage.Host{Name: "cp", Exec: &scriptHost{out: map[string]string{"if ! ": "sudo\nrunc"}}}
	st, err := probe(context.Background(), h, BaseHost{User: "cp-dev", Runc: "1.4.0"}.checks())
	if err != nil {
		t.Fatal(err)
	}
	if st.Done || st.Detail != "not yet: sudo, runc" {
		t.Fatalf("got %+v", st)
	}
	h.Exec = &scriptHost{}
	if st, _ := probe(context.Background(), h, BaseHost{User: "cp-dev"}.checks()); !st.Done {
		t.Fatalf("no failed check, but not done: %+v", st)
	}
}

// A probe is read-only: it runs one command and writes nothing.
func TestProbeWritesNothing(t *testing.T) {
	sh := &scriptHost{}
	h := &stage.Host{Name: "cp", Exec: sh}
	for _, s := range []stage.Stage{PrivateNetwork{}, BaseHost{}, WireGuardHub{}, KubePrep{}, KubeadmInit{ServiceCIDR: "10.96.0.0/16"},
		Flannel{}, CloudController{}, CSI{}} {
		if _, err := s.Probe(context.Background(), h); err != nil {
			t.Fatalf("%s: %v", s.Name(), err)
		}
	}
	if len(sh.written) > 0 {
		t.Fatalf("a probe wrote %v", sh.written)
	}
	for _, c := range sh.ran {
		if strings.Contains(c, scriptDir) || strings.Contains(c, "kubectl apply") {
			t.Fatalf("a probe ran a write: %s", c)
		}
	}
}

func TestKubeadmSANCheck(t *testing.T) {
	k := KubeadmInit{Endpoint: "k8s-cp.tosak.internal", PrivateIP: "10.0.1.5", VpnIP: "10.100.0.1", PublicIP: "46.62.209.249", ServiceCIDR: "10.96.0.0/16"}
	want, err := k.wantSANs()
	if err != nil {
		t.Fatal(err)
	}
	line := "[certs] apiserver serving cert is signed for DNS names [k8s-cp k8s-cp.tosak.internal kubernetes kubernetes.default kubernetes.default.svc kubernetes.default.svc.cluster.local] and IPs [10.96.0.1 10.0.1.5 10.100.0.1 46.62.209.249]"
	if m := missingSANs(line, want); len(m) != 0 {
		t.Fatalf("the runbook's line misses %v", m)
	}
	// The /12 default puts the first service address at 10.96.0.1 too, so
	// it is the VPN address that tells a wrong certificate apart.
	noVPN := strings.Replace(line, " 10.100.0.1", "", 1)
	if m := missingSANs(noVPN, want); len(m) != 1 || m[0] != "10.100.0.1" {
		t.Fatalf("want 10.100.0.1 missing, got %v", m)
	}
}

func TestKubeadmInitNeverShowsTheJoinToken(t *testing.T) {
	k := KubeadmInit{Endpoint: "e", PrivateIP: "p", VpnIP: "v", PublicIP: "x", PodCIDR: "a", ServiceCIDR: "10.96.0.0/16"}
	for log, s := range map[string]string{initLog: k.init(), dryRunLog: k.dryRun()} {
		if !strings.Contains(s, "> "+log+" 2>&1") || !strings.Contains(s, noToken(log)) || strings.Contains(s, "| grep 'apiserver") {
			t.Fatalf("kubeadm output is not kept from the terminal, or a pipe hides its error:\n%s", s)
		}
	}
}

func TestCloudControllerKeepsTheTokenOffDisk(t *testing.T) {
	sh := &scriptHost{}
	c := CloudController{Token: "secret-token", Network: "tosak-net", Manifest: "kind: List"}
	if err := c.Act(context.Background(), &stage.Host{Exec: sh}); err != nil {
		t.Fatal(err)
	}
	if sh.written[tokenFile] != "secret-token" {
		t.Fatalf("token not written to %s", tokenFile)
	}
	for p, content := range sh.written {
		if p != tokenFile && strings.Contains(content, "secret-token") {
			t.Fatalf("the token reached %s", p)
		}
	}
	for _, cmd := range sh.ran {
		if strings.Contains(cmd, "secret-token") {
			t.Fatalf("the token is on a command line: %s", cmd)
		}
	}
	if !strings.Contains(c.script(), "trap 'rm -f "+tokenFile+"' EXIT") {
		t.Fatal("the token file is not removed")
	}
}

func TestCompleteBuildReadsTheMarker(t *testing.T) {
	h := &stage.Host{Exec: &scriptHost{out: map[string]string{"cat " + MarkerPath: MarkerRunning}}}
	if st, _ := (CompleteBuild{}).Probe(context.Background(), h); st.Done {
		t.Fatal("a running build reads as complete")
	}
	h.Exec = &scriptHost{out: map[string]string{"cat " + MarkerPath: MarkerComplete}}
	if st, _ := (CompleteBuild{}).Probe(context.Background(), h); !st.Done {
		t.Fatal("a complete build reads as not done")
	}
}

func TestPlanNewPreviewsEveryStageWithoutAHost(t *testing.T) {
	var b strings.Builder
	all := []stage.Stage{RotateHostKey{}, PrivateNetwork{}, BaseHost{User: "cp-dev"}, WireGuardHub{}, KubePrep{}, KubeadmInit{ServiceCIDR: "10.96.0.0/16"},
		Flannel{}, CloudController{}, CSI{}, CompleteBuild{}}
	if err := stage.PlanNew(context.Background(), all, "k8s-cp", &b); err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if !strings.Contains(b.String(), "[k8s-cp] "+s.Name()+": would run — "+s.Runbook()) {
			t.Errorf("no preview of %s", s.Name())
		}
	}
}
