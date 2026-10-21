package stages

import (
	"context"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kubeconfig"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/local"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/wgconf"
)

func laptopHost(t *testing.T) *stage.Host {
	return &stage.Host{Name: "laptop", Exec: local.Exec{TmpDir: t.TempDir()}}
}

func TestLaptopWireGuardSeedsAndEditsTheStandIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rehearsal", "wg0.conf")
	l := LaptopWireGuard{
		Path:  path,
		HubIP: netip.MustParseAddr("10.100.0.1"),
		Hub:   wgconf.HubPeer{PublicKey: "hubKey=", Endpoint: "198.51.100.4:51820"},
		Seed:  wgconf.RehearsalCopy("10.100.0.69/32", "10.100.0.0/24"),
	}
	if err := stage.Run(context.Background(), []stage.Stage{l}, laptopHost(t), stage.Options{Apply: true, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "PublicKey = hubKey=\nEndpoint = 198.51.100.4:51820") {
		t.Fatalf("stand-in not edited:\n%s", b)
	}
	// A second build has a new hub key: the edit leaves a backup.
	l.Hub.PublicKey = "nextKey="
	if err := stage.Run(context.Background(), []stage.Stage{l}, laptopHost(t), stage.Options{Apply: true, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	backups, _ := filepath.Glob(path + ".bak-kluster-*")
	if len(backups) != 1 {
		t.Fatalf("want one backup, got %v", backups)
	}
}

func TestLaptopWireGuardRefusesAMissingLiveFile(t *testing.T) {
	l := LaptopWireGuard{Path: filepath.Join(t.TempDir(), "wg0.conf"), HubIP: netip.MustParseAddr("10.100.0.1")}
	if _, err := l.Probe(context.Background(), laptopHost(t)); err == nil {
		t.Fatal("a missing live config must be an error, not a file to create")
	}
}

const adminConf = `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: Q0EtTkVX
    server: https://k8s-cp.tosak.internal:6443
  name: kubernetes
contexts:
- context:
    cluster: kubernetes
    user: kubernetes-admin
  name: kubernetes-admin@kubernetes
current-context: kubernetes-admin@kubernetes
kind: Config
users:
- name: kubernetes-admin
  user:
    client-certificate-data: Q0VSVA==
    client-key-data: S0VZ
`

func TestLaptopKubeconfigWritesAStandAlone(t *testing.T) {
	names := kubeconfig.NamesFor("tosak-rehearsal")
	conf, err := kubeconfig.ForLaptop([]byte(adminConf), names, "https://10.100.0.1:6443")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "kubeconfig")
	l := LaptopKubeconfig{Path: path, Names: names, Conf: conf}
	if err := stage.Run(context.Background(), []stage.Stage{l}, laptopHost(t), stage.Options{Apply: true, Out: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("kubeconfig mode %v", st.Mode().Perm())
	}
	preview, _ := l.Preview(context.Background(), laptopHost(t))
	if strings.Contains(preview, "S0VZ") {
		t.Fatal("the preview shows the client key")
	}
}

// countingExec is the laptop, counting the writes of each file and
// recording each command.
type countingExec struct {
	local.Exec
	writes map[string]int
	ran    []string
}

func (c *countingExec) WriteFile(ctx context.Context, path, content string, mode os.FileMode) error {
	c.writes[path]++
	return c.Exec.WriteFile(ctx, path, content, mode)
}

func (c *countingExec) Run(ctx context.Context, cmd string) (string, error) {
	c.ran = append(c.ran, cmd)
	return c.Exec.Run(ctx, cmd)
}

// mergeSetup writes a live kubeconfig holding a dead cluster under the
// names "tosak", with current-context current, and the merge of a new
// cluster at port 1 of the loopback, which refuses at once, standing in for
// a context that does not answer. Every kubectl call names a file under the
// test's directory.
func mergeSetup(t *testing.T, current string) (LaptopKubeconfig, *countingExec) {
	t.Helper()
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("kubectl not on PATH")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	old := strings.NewReplacer("Q0EtTkVX", "Q0EtT0xE", "name: kubernetes-admin@kubernetes", "name: tosak-admin@tosak",
		"kubernetes-admin@kubernetes", "tosak-admin@tosak",
		"kubernetes-admin", "tosak-admin", "name: kubernetes", "name: tosak", "cluster: kubernetes", "cluster: tosak",
		"https://k8s-cp.tosak.internal:6443", "https://10.100.0.1:6443").Replace(adminConf)
	old = strings.Replace(old, "current-context: tosak-admin@tosak", "current-context: "+current, 1)
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	names := kubeconfig.NamesFor("tosak")
	conf, _ := kubeconfig.ForLaptop([]byte(adminConf), names, "https://127.0.0.1:1")
	run := filepath.Join(dir, "run")
	if err := os.Mkdir(run, 0o700); err != nil {
		t.Fatal(err)
	}
	l := LaptopKubeconfig{Path: path, Merge: true, Names: names, Conf: conf, TmpDir: run}
	return l, &countingExec{Exec: local.Exec{TmpDir: run}, writes: map[string]int{}}
}

func readEntries(t *testing.T, l LaptopKubeconfig) kubeconfig.Entries {
	t.Helper()
	b, _ := os.ReadFile(l.Path)
	e, err := kubeconfig.Find(b, l.Names)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// The merge replaces the dead cluster's entries under the same names, and
// leaves current-context alone when the new context does not answer. The
// deletes run on a copy, and the live file is written once.
func TestLaptopKubeconfigMergeReplacesOldEntriesAndWaitsForAnAnswer(t *testing.T) {
	l, x := mergeSetup(t, "other")
	err := l.Act(context.Background(), &stage.Host{Name: "laptop", Exec: x})
	if err == nil || !strings.Contains(err.Error(), "current-context was not moved") {
		t.Fatalf("want the merge to stop before use-context, got %v", err)
	}
	e := readEntries(t, l)
	if e.Cluster == nil || e.Cluster.CAData != "Q0EtTkVX" || e.Cluster.Server != "https://127.0.0.1:1" {
		t.Fatalf("the dead cluster's entry survived: %+v", e.Cluster)
	}
	if e.CurrentContext != "other" {
		t.Fatalf("current-context moved to %q before the new context answered", e.CurrentContext)
	}
	if backups, _ := filepath.Glob(l.Path + ".bak-kluster-*"); len(backups) != 1 {
		t.Fatalf("want one backup, got %v", backups)
	}
	if x.writes[l.Path] != 1 {
		t.Fatalf("the live file was written %d times, want once", x.writes[l.Path])
	}
	deletes := 0
	for _, c := range x.ran {
		if strings.Contains(c, "config delete-") {
			deletes++
			if strings.Contains(c, local.Quote(l.Path)+" ") {
				t.Fatalf("a delete ran on the live file: %s", c)
			}
		}
	}
	if deletes != 3 {
		t.Fatalf("ran %d deletes, want 3: %v", deletes, x.ran)
	}
	if left, _ := filepath.Glob(filepath.Join(l.TmpDir, "*")); len(left) != 0 {
		t.Fatalf("the run directory keeps %v", left)
	}
}

// When the live current-context is the new context's own name, deleting it
// from the copy must not leave the merge without one: the name stays
// current, now naming the new cluster's entry, and the error says so.
func TestLaptopKubeconfigMergeKeepsACurrentContextOfTheSameName(t *testing.T) {
	l, x := mergeSetup(t, "tosak-admin@tosak")
	err := l.Act(context.Background(), &stage.Host{Name: "laptop", Exec: x})
	if err == nil || !strings.Contains(err.Error(), "current-context already named tosak-admin@tosak") {
		t.Fatalf("got %v", err)
	}
	e := readEntries(t, l)
	if e.CurrentContext != "tosak-admin@tosak" || e.Cluster == nil || e.Cluster.Server != "https://127.0.0.1:1" {
		t.Fatalf("current-context %q, cluster %+v", e.CurrentContext, e.Cluster)
	}
	if x.writes[l.Path] != 1 {
		t.Fatalf("the live file was written %d times, want once", x.writes[l.Path])
	}
}
