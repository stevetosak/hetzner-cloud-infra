package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/db"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/pgcluster"
)

func TestDBTargetTime(t *testing.T) {
	defer func(f, r bool, tt string) { dbFlags.fresh, dbFlags.rehearsed, dbFlags.targetTime = f, r, tt }(
		dbFlags.fresh, dbFlags.rehearsed, dbFlags.targetTime)
	for _, tc := range []struct {
		time      string
		rehearsed bool
		mode      db.Mode
		live      bool
		refused   string
	}{
		{"", false, db.Recover, true, ""},
		{"2026-10-24T10:00:00Z", false, db.Recover, false, ""},
		{"2026-10-24T10:00:00Z", true, db.Recover, true, ""},
		{"2026-10-24T10:00:00Z", false, db.Recover, true, "add --rehearsed"},
		{"2026-10-24T12:00:00+02:00", true, db.Recover, false, "in UTC"},
		{"2026-10-24T10:00:00Z", false, db.Fresh, false, "is for --recover"},
		{"", true, db.Recover, true, "goes with --target-time"},
	} {
		dbFlags.targetTime, dbFlags.rehearsed = tc.time, tc.rehearsed
		got, err := dbTargetTime(tc.mode, tc.live)
		switch {
		case tc.refused == "" && err != nil:
			t.Errorf("%+v: %v", tc, err)
		case tc.refused != "" && (err == nil || !strings.Contains(err.Error(), tc.refused)):
			t.Errorf("%+v: %v, want %q", tc, err, tc.refused)
		case tc.refused == "" && (got != nil) != (tc.time != ""):
			t.Errorf("%+v: time %v", tc, got)
		}
	}
}

// itemVault answers with the item name the key is read from.
type itemVault struct{}

func (itemVault) Issued(_ context.Context, k *catalog.Key) ([]byte, error) {
	return []byte(k.Item + "." + k.Field), nil
}

// TestStoreTokensComeFromTheRightItems proves, on the repository's catalog
// and ObjectStores, that a rehearsal reads only rehearsal items.
func TestStoreTokensComeFromTheRightItems(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	cat, err := catalog.Load(filepath.Join(root, catalog.FileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		live            bool
		archive, source string
	}{
		{true, "kluster/r2-backup.access_key_id", "kluster/r2-readonly."},
		{false, "kluster/r2-backup-rehearsal-rw.access_key_id", "kluster/r2-backup-rehearsal-ro."},
	} {
		as, ss, err := pgcluster.Stores(root, tc.live)
		if err != nil {
			t.Fatal(err)
		}
		a, err := storeCredentials(context.Background(), cat, itemVault{}, as, tc.live)
		if err != nil {
			t.Fatal(err)
		}
		s, err := storeCredentials(context.Background(), cat, itemVault{}, ss, tc.live)
		if err != nil {
			t.Fatal(err)
		}
		if a.AccessKeyID != tc.archive || !strings.HasPrefix(s.AccessKeyID, tc.source) || !strings.HasPrefix(s.SecretAccessKey, tc.source) {
			t.Errorf("live=%v: archive %q, source %q/%q", tc.live, a.AccessKeyID, s.AccessKeyID, s.SecretAccessKey)
		}
	}
}
