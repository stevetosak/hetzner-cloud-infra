package meshstore

import (
	"context"
	"errors"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/nodeset"
)

// The public keys of the tests: 32 bytes in base64, no real machine's.
const (
	keyA = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	keyB = "HxwdHhsaGRgXFhUUExIREA8ODQwLCgkIBwYFBAMCAQA="
)

// fakeCluster is a ConfigMap in memory.
type fakeCluster struct {
	data    map[string]string
	exists  bool
	creates int
	patches []string
	err     error
}

func (f *fakeCluster) ConfigMap(context.Context, string, string) (map[string]string, bool, error) {
	return f.data, f.exists, f.err
}

func (f *fakeCluster) CreateConfigMap(context.Context, string, string) error {
	f.creates++
	f.exists = true
	f.data = map[string]string{}
	return nil
}

func (f *fakeCluster) PatchConfigMapData(_ context.Context, _, _, key, value string) error {
	f.patches = append(f.patches, key)
	f.data[key] = value
	return nil
}

func entry(name, site, addr, key string) Entry {
	return Entry{Name: name, Site: site, Address: netip.MustParseAddr(addr), PublicKey: key}
}

func TestPutThenEntries(t *testing.T) {
	f := &fakeCluster{}
	s := ConfigMap{Client: f}
	ctx := context.Background()
	want := entry("box1", "ts1", "10.101.1.1", keyA)
	if err := s.Put(ctx, want); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 {
		t.Errorf("creates = %d", f.creates)
	}
	got, err := s.Entries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["box1"] != want {
		t.Errorf("got %+v, want %+v", got["box1"], want)
	}
}

func TestPutKeepsTheOtherEntries(t *testing.T) {
	f := &fakeCluster{}
	s := ConfigMap{Client: f}
	ctx := context.Background()
	for _, e := range []Entry{entry("box1", "ts1", "10.101.1.1", keyA), entry("wk1", "hel1", "10.101.0.11", keyB)} {
		if err := s.Put(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if f.creates != 1 {
		t.Errorf("the second Put made the ConfigMap again: %d", f.creates)
	}
	if !slices.Equal(f.patches, []string{"box1", "wk1"}) {
		t.Errorf("patches = %v: each Put writes only its own key", f.patches)
	}
	if got := slices.Sorted(maps.Keys(f.data)); !slices.Equal(got, []string{"box1", "wk1"}) {
		t.Errorf("keys = %v", got)
	}
}

func TestEntriesOfAMissingStoreAreEmpty(t *testing.T) {
	got, err := ConfigMap{Client: &fakeCluster{}}.Entries(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestEntriesRefusesAMalformedEntry(t *testing.T) {
	f := &fakeCluster{exists: true, data: map[string]string{"box1": "not json"}}
	if _, err := (ConfigMap{Client: f}).Entries(context.Background()); err == nil || !strings.Contains(err.Error(), "box1") {
		t.Fatalf("got %v", err)
	}
}

func TestEntriesPassesAClusterError(t *testing.T) {
	boom := errors.New("boom")
	if _, err := (ConfigMap{Client: &fakeCluster{err: boom}}).Entries(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

func TestPutRefusesABadKey(t *testing.T) {
	for name, key := range map[string]string{
		"empty":      "",
		"not base64": "not a key",
		"too short":  "AAECAw==",
		"whitespace": keyA + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeCluster{}
			err := ConfigMap{Client: f}.Put(context.Background(), entry("box1", "ts1", "10.101.1.1", key))
			if err == nil || !strings.Contains(err.Error(), "box1 has no valid WireGuard public key") {
				t.Fatalf("got %v", err)
			}
			if f.creates != 0 || len(f.patches) != 0 {
				t.Error("a refused key still wrote to the cluster")
			}
		})
	}
}

func testMesh(t *testing.T) *nodeset.Mesh {
	t.Helper()
	s, err := nodeset.Parse("live.yaml", []byte(`
sites:
  hel1: {provider: hcloud, reliability: high, reach: public, meshSubnet: 10.101.0.0/24}
  ts1: {provider: self-hosted, reliability: best-effort, reach: nat, meshSubnet: 10.101.1.0/24}
machines:
  wk1: {site: hel1, provisioner: hcloud, meshAddress: 10.101.0.11}
  box1: {site: ts1, provisioner: byo, meshAddress: 10.101.1.1}
`))
	if err != nil {
		t.Fatal(err)
	}
	return s.Mesh()
}

func TestCheckAcceptsAMatchingStore(t *testing.T) {
	entries := map[string]Entry{"wk1": entry("wk1", "hel1", "10.101.0.11", keyA)}
	if err := Check(entries, testMesh(t)); err != nil {
		t.Fatalf("a machine without a key yet must pass: %v", err)
	}
}

func TestCheckRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		e    Entry
		want string
	}{
		"undeclared":   {entry("wk9", "hel1", "10.101.0.19", keyA), "wk9 is not a machine of the Node set's mesh"},
		"other site":   {entry("box1", "hel1", "10.101.1.1", keyA), `box1 has site "hel1", but the Node set says ts1`},
		"other addr":   {entry("box1", "ts1", "10.101.1.9", keyA), "box1 has address 10.101.1.9, but the Node set says 10.101.1.1"},
		"bad key":      {entry("box1", "ts1", "10.101.1.1", "nope"), "box1 has no valid WireGuard public key"},
		"zero address": {Entry{Name: "box1", Site: "ts1", PublicKey: keyA}, "box1 has address invalid IP"},
	} {
		t.Run(name, func(t *testing.T) {
			err := Check(map[string]Entry{tc.e.Name: tc.e}, testMesh(t))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal naming %q, got %v", tc.want, err)
			}
		})
	}
}
