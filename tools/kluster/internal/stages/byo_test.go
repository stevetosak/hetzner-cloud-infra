package stages

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// The byo check in real bash, with hostname and dpkg stubbed: the laptop's
// own release passes, and each other fact fails by its name.
func TestByoCheckInBash(t *testing.T) {
	release := laptopRelease(t)
	ctx := context.Background()
	for _, c := range []struct {
		name, host, arch, release, detail string
	}{
		{"fits", "tswk1", "amd64", release, ""},
		{"hostname", "ubuntu", "amd64", release, "not yet: hostname tswk1"},
		{"arch", "tswk1", "arm64", release, "not yet: amd64"},
		{"release", "tswk1", "amd64", "20.04", "not yet: ubuntu 20.04"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sh := stubbedCommands(t, map[string]string{"hostname": "echo " + c.host, "dpkg": "echo " + c.arch})
			st, err := ByoCheck{Node: "tswk1", Release: c.release, ClusterCA: "x"}.Probe(ctx, &stage.Host{Exec: sh})
			if err != nil {
				t.Fatal(err)
			}
			if c.detail == "" && !st.Done || c.detail != "" && st.Detail != c.detail {
				t.Errorf("got %+v, want %q", st, c.detail)
			}
		})
	}
}

func laptopRelease(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		t.Skip("no /etc/os-release")
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "VERSION_ID="); ok {
			if !strings.Contains(string(b), "\nID=ubuntu\n") {
				t.Skip("not Ubuntu")
			}
			return strings.Trim(v, `"`)
		}
	}
	t.Skip("no VERSION_ID")
	return ""
}

// stubbedCommands is real bash with the given commands stubbed first on PATH.
func stubbedCommands(t *testing.T, stubs map[string]string) stubbedBash {
	t.Helper()
	dir := t.TempDir()
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/usr/bin/env bash\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return stubbedBash{dir: dir}
}

// A machine without a CA, or with the cluster's, passes; one that holds
// another cluster's CA does not.
func TestByoCheckClusterCAInBash(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.crt")
	sum := sha256.Sum256([]byte("cluster CA\n"))
	ours := hex.EncodeToString(sum[:])
	sh := stubbedCommands(t, nil)
	ctx := context.Background()
	holds := func() bool {
		_, err := sh.Run(ctx, caCheck(ca, ours))
		return err == nil
	}
	if !holds() {
		t.Error("a machine with no CA failed")
	}
	if err := os.WriteFile(ca, []byte("cluster CA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !holds() {
		t.Error("the cluster's own CA failed")
	}
	if err := os.WriteFile(ca, []byte("rehearsal CA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if holds() {
		t.Error("another cluster's CA passed")
	}
}

func TestByoCheckActRefusesWithWhatIsMissing(t *testing.T) {
	sh := &scriptHost{out: map[string]string{"if ! ": "hostname tswk1\namd64"}}
	err := ByoCheck{Node: "tswk1", Release: "24.04"}.Act(context.Background(), &stage.Host{Exec: sh})
	if err == nil || !strings.Contains(err.Error(), "it lacks hostname tswk1, amd64") {
		t.Fatalf("got %v", err)
	}
	for _, cmd := range sh.ran {
		if !strings.HasPrefix(cmd, "if ! ") {
			t.Errorf("the check ran %q", cmd)
		}
	}
}

// registration is a Node that exists once the machine's kubelet restarted.
type registration struct{ registered bool }

// kubeletMachine records its commands; a kubelet restart registers the Node.
type kubeletMachine struct {
	scriptHost
	r *registration
}

func (m *kubeletMachine) Run(ctx context.Context, cmd string) (string, error) {
	if cmd == "systemctl restart kubelet" {
		m.r.registered = true
	}
	return m.scriptHost.Run(ctx, cmd)
}

// registrationCP answers the Node list from the registration.
type registrationCP struct {
	scriptHost
	r *registration
}

func (c *registrationCP) Run(context.Context, string) (string, error) {
	if c.r.registered {
		return `{"items":[{"metadata":{"name":"tswk1"}}]}`, nil
	}
	return `{"items":[{"metadata":{"name":"k8s-cp"}}]}`, nil
}

func TestNodeRegisteredRestartsTheKubeletOnlyWhenTheNodeIsGone(t *testing.T) {
	r := &registration{}
	machine := &kubeletMachine{r: r}
	n := NodeRegistered{ControlPlane: &registrationCP{r: r}, Node: "tswk1", Every: time.Millisecond, Timeout: time.Second}
	h := &stage.Host{Name: "tswk1", Exec: machine}
	opts := stage.Options{Apply: true, Out: &strings.Builder{}}
	if err := stage.Run(context.Background(), []stage.Stage{n}, h, opts); err != nil {
		t.Fatal(err)
	}
	if strings.Join(machine.ran, "\n") != "systemctl restart kubelet" {
		t.Errorf("the machine ran %q", machine.ran)
	}
	machine.ran = nil
	if err := stage.Run(context.Background(), []stage.Stage{n}, h, opts); err != nil {
		t.Fatal(err)
	}
	if len(machine.ran) != 0 {
		t.Errorf("a registered Node restarted the kubelet: %q", machine.ran)
	}
}

func TestNodeRegisteredTimesOut(t *testing.T) {
	cp := &scriptHost{out: map[string]string{"": `{"items":[]}`}}
	n := NodeRegistered{ControlPlane: cp, Node: "tswk1", Every: time.Millisecond, Timeout: 20 * time.Millisecond}
	err := n.Act(context.Background(), &stage.Host{Exec: &scriptHost{}})
	if err == nil || !strings.Contains(err.Error(), "did not register") {
		t.Fatalf("got %v", err)
	}
}

func TestSiteFirewallScript(t *testing.T) {
	s := SiteFirewall{}.script()
	check := strings.Index(s, "nft -c -f "+siteFirewallFile)
	enable := strings.Index(s, "systemctl enable "+siteFirewallUnit)
	if check < 0 || enable < 0 || check > enable {
		t.Errorf("the rules are not checked before the unit is enabled:\n%s", s)
	}
	if strings.Contains(s, "flush ruleset") || strings.Contains(siteFirewallService, "flush") {
		t.Error("the firewall flushes rules that are not its own")
	}
	if strings.Contains(SiteFirewall{}.File(), "dport 51821") {
		t.Error("a NAT machine opens the mesh port")
	}
	if !strings.Contains(SiteFirewall{Public: true}.File(), "\t\tudp dport 51821 accept\n") {
		t.Error("a public machine does not open the mesh port")
	}
	if (SiteFirewall{}).hash() == (SiteFirewall{Public: true}).hash() {
		t.Error("two rule sets have one hash")
	}
}
