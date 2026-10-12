package bitwarden

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeVault answers rbw and bw calls from a table and records them.
type fakeVault struct {
	locked  bool
	items   map[string]string // id → `rbw get --raw` JSON
	names   map[string]string // id → name, for `rbw list --raw`
	files   map[string]string // attachment file name → content
	syncErr error
	bwErr   error
	calls   []string
}

func (f *fakeVault) rbw(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, "rbw "+strings.Join(args, " "))
	switch args[0] {
	case "unlocked":
		if f.locked {
			return nil, errors.New("exit status 1")
		}
		return nil, nil
	case "sync":
		return nil, f.syncErr
	case "list":
		var rows []string
		for id, name := range f.names {
			rows = append(rows, `{"id":"`+id+`","name":"`+name+`","user":null,"folder":null,"type":"Note"}`)
		}
		return []byte("[" + strings.Join(rows, ",") + "]"), nil
	case "get":
		if j, ok := f.items[args[2]]; ok {
			return []byte(j), nil
		}
		return nil, errors.New("no entry found")
	}
	return nil, errors.New("unexpected call")
}

func (f *fakeVault) bw(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, "bw "+strings.Join(args, " "))
	if f.bwErr != nil {
		return nil, f.bwErr
	}
	if args[0] == "get" {
		return []byte(f.files[args[2]]), nil
	}
	return nil, nil
}

func (f *fakeVault) client(warn *bytes.Buffer) *Client {
	if warn == nil {
		warn = &bytes.Buffer{}
	}
	return NewWithRunners(f.rbw, f.bw, warn)
}

const liveItem = `{"id":"i1","folder":null,"name":"kluster/live","data":null,
 "fields":[{"name":"hcloud_token","value":"tok","type":"hidden"},{"name":"empty","value":"","type":"hidden"},{"name":null,"value":"x","type":"text"}],
 "notes":null,"history":[]}`

func vault() *fakeVault {
	return &fakeVault{
		items: map[string]string{"i1": liveItem},
		names: map[string]string{"i1": "kluster/live"},
		files: map[string]string{"keystore.p12": "P12"},
	}
}

func TestItemReadsFieldsAndAttachments(t *testing.T) {
	f := vault()
	c := f.client(nil)
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
	if _, err := it.Field("password"); err == nil {
		t.Error("a note has no password")
	}
	if b, err := c.Attachment(context.Background(), it, "keystore.p12"); err != nil || string(b) != "P12" {
		t.Errorf("Attachment = %q, %v", b, err)
	}
	want := "rbw unlocked|rbw sync|rbw list --raw|rbw get --raw i1|bw sync|bw get attachment keystore.p12 --itemid i1 --raw"
	if got := strings.Join(f.calls, "|"); got != want {
		t.Errorf("calls = %s", got)
	}
}

func TestLoginPassword(t *testing.T) {
	f := vault()
	f.items["i1"] = `{"id":"i1","name":"kluster/live","data":{"username":"u","password":"pw","totp":null,"uris":[]},"fields":[]}`
	it, err := f.client(nil).Item(context.Background(), "kluster/live")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := it.Field("password"); err != nil || v != "pw" {
		t.Errorf("password = %q, %v", v, err)
	}
}

// `rbw get` falls back to a part match: only the exact name counts.
func TestItemNeedsTheExactName(t *testing.T) {
	_, err := vault().client(nil).Item(context.Background(), "kluster/liv")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestItemRefusesADuplicateName(t *testing.T) {
	f := vault()
	f.names["i2"] = "kluster/live"
	_, err := f.client(nil).Item(context.Background(), "kluster/live")
	if err == nil || !strings.Contains(err.Error(), "2 items named") {
		t.Fatalf("err = %v", err)
	}
}

func TestItemRefusesAnotherItem(t *testing.T) {
	f := vault()
	f.items["i1"] = strings.Replace(liveItem, `"name":"kluster/live"`, `"name":"kluster/other"`, 1)
	_, err := f.client(nil).Item(context.Background(), "kluster/live")
	if err == nil || !strings.Contains(err.Error(), "another item") {
		t.Fatalf("err = %v", err)
	}
}

// One check, one sync and one list serve every item; each item is read once.
func TestOneListAndOneReadPerItem(t *testing.T) {
	f := vault()
	f.items["i2"] = strings.NewReplacer(`"id":"i1"`, `"id":"i2"`, `"name":"kluster/live"`, `"name":"kluster/age-key"`).Replace(liveItem)
	f.names["i2"] = "kluster/age-key"
	c := f.client(nil)
	for _, n := range []string{"kluster/live", "kluster/age-key", "kluster/live"} {
		it, err := c.Item(context.Background(), n)
		if err != nil || it.Name != n {
			t.Fatalf("%s: %v, %v", n, it, err)
		}
	}
	if got := strings.Join(f.calls, "|"); got != "rbw unlocked|rbw sync|rbw list --raw|rbw get --raw i1|rbw get --raw i2" {
		t.Errorf("calls = %s", got)
	}
}

func TestItemNotFound(t *testing.T) {
	_, err := (&fakeVault{}).client(nil).Item(context.Background(), "kluster/live")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

// A locked profile stops with the unlock command; kluster never unlocks.
func TestLockedStopsWithTheUnlockCommand(t *testing.T) {
	f := vault()
	f.locked = true
	err := f.client(nil).Ready(context.Background())
	if err == nil || !strings.Contains(err.Error(), "RBW_PROFILE=kluster rbw unlock") {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("calls = %v", f.calls)
	}
}

// The local copy decrypts offline, so a failed sync warns and goes on.
func TestFailedSyncWarns(t *testing.T) {
	f := vault()
	f.syncErr = errors.New("offline")
	var warn bytes.Buffer
	if _, err := f.client(&warn).Item(context.Background(), "kluster/live"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warn.String(), "sync failed") {
		t.Errorf("warn = %q", warn.String())
	}
}

// bw is reached for attachments only, and a locked bw names its unlock.
func TestAttachmentNeedsBw(t *testing.T) {
	f := vault()
	f.bwErr = errors.New("Vault is locked.")
	c := f.client(nil)
	it, err := c.Item(context.Background(), "kluster/live")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Attachment(context.Background(), it, "keystore.p12")
	if err == nil || !strings.Contains(err.Error(), "BW_SESSION") {
		t.Fatalf("err = %v", err)
	}
}
