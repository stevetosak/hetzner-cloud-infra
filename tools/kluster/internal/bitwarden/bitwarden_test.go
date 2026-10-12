package bitwarden

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeVault answers `bw` calls from a table and records them.
type fakeVault struct {
	status  string
	items   map[string]string // item name → JSON
	files   map[string]string // attachment id → content
	syncErr error
	calls   []string
}

func (f *fakeVault) run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "status":
		return []byte(`{"status":"` + f.status + `"}`), nil
	case "sync":
		return nil, f.syncErr
	case "list":
		var hits []string
		for name, j := range f.items {
			if strings.Contains(name, args[3]) {
				hits = append(hits, j)
			}
		}
		return []byte("[" + strings.Join(hits, ",") + "]"), nil
	case "get":
		return []byte(f.files[args[2]]), nil
	}
	return nil, errors.New("unexpected call")
}

const liveItem = `{"id":"i1","name":"kluster/live","login":null,
 "fields":[{"name":"hcloud_token","value":"tok"},{"name":"empty","value":""}],
 "attachments":[{"id":"a1","fileName":"keystore.p12"}]}`

func TestItemReadsFieldsAndAttachments(t *testing.T) {
	f := &fakeVault{status: "unlocked", items: map[string]string{"kluster/live": liveItem}, files: map[string]string{"a1": "P12"}}
	c := NewWithRunner(f.run, &bytes.Buffer{})
	it, err := c.Item(context.Background(), "kluster/live")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := it.Field("hcloud_token"); err != nil || v != "tok" {
		t.Errorf("Field = %q, %v", v, err)
	}
	if _, err := it.Field("empty"); err == nil {
		t.Error("an empty field must be an error")
	}
	if _, err := it.Field("missing"); err == nil {
		t.Error("a missing field must be an error")
	}
	if b, err := c.Attachment(context.Background(), it, "keystore.p12"); err != nil || string(b) != "P12" {
		t.Errorf("Attachment = %q, %v", b, err)
	}
	if got := strings.Join(f.calls, "|"); got != "status|sync|list items --search kluster/|get attachment a1 --itemid i1 --raw" {
		t.Errorf("calls = %s", got)
	}
}

// The search matches parts of names: only the exact name counts.
func TestItemNeedsTheExactName(t *testing.T) {
	f := &fakeVault{status: "unlocked", items: map[string]string{"kluster/live": liveItem}}
	_, err := NewWithRunner(f.run, &bytes.Buffer{}).Item(context.Background(), "kluster/liv")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestItemRefusesADuplicateName(t *testing.T) {
	f := &fakeVault{status: "unlocked", items: map[string]string{
		"kluster/live":   liveItem,
		"kluster/live-2": strings.Replace(liveItem, `"id":"i1"`, `"id":"i2"`, 1),
	}}
	_, err := NewWithRunner(f.run, &bytes.Buffer{}).Item(context.Background(), "kluster/live")
	if err == nil || !strings.Contains(err.Error(), "2 items named") {
		t.Fatalf("err = %v", err)
	}
}

// One status, one sync and one list serve every item under a prefix.
func TestItemsUnderOnePrefixCostOneList(t *testing.T) {
	other := strings.NewReplacer(`"id":"i1"`, `"id":"i2"`, `"name":"kluster/live"`, `"name":"kluster/age-key"`).Replace(liveItem)
	f := &fakeVault{status: "unlocked", items: map[string]string{"kluster/live": liveItem, "kluster/age-key": other}}
	c := NewWithRunner(f.run, &bytes.Buffer{})
	for _, n := range []string{"kluster/live", "kluster/age-key", "kluster/live"} {
		it, err := c.Item(context.Background(), n)
		if err != nil || it.Name != n {
			t.Fatalf("%s: %v, %v", n, it, err)
		}
	}
	if got := strings.Join(f.calls, "|"); got != "status|sync|list items --search kluster/" {
		t.Errorf("calls = %s", got)
	}
}

func TestItemNotFound(t *testing.T) {
	f := &fakeVault{status: "unlocked"}
	_, err := NewWithRunner(f.run, &bytes.Buffer{}).Item(context.Background(), "kluster/live")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestLockedVaultStopsWithTheUnlockCommand(t *testing.T) {
	for _, st := range []string{"locked", "unauthenticated"} {
		f := &fakeVault{status: st}
		err := NewWithRunner(f.run, &bytes.Buffer{}).Ready(context.Background())
		if err == nil || !strings.Contains(err.Error(), "bw unlock --raw") {
			t.Errorf("%s: err = %v", st, err)
		}
	}
}

// The local cache decrypts offline, so a failed sync warns and goes on, and
// a second read does not sync again.
func TestFailedSyncWarnsAndSyncsOnce(t *testing.T) {
	f := &fakeVault{status: "unlocked", syncErr: errors.New("offline"), items: map[string]string{"kluster/live": liveItem}}
	var warn bytes.Buffer
	c := NewWithRunner(f.run, &warn)
	for range 2 {
		if _, err := c.Item(context.Background(), "kluster/live"); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(warn.String(), "sync failed") {
		t.Errorf("warn = %q", warn.String())
	}
	syncs := 0
	for _, call := range f.calls {
		if call == "sync" {
			syncs++
		}
	}
	if syncs != 1 {
		t.Errorf("synced %d times", syncs)
	}
}
