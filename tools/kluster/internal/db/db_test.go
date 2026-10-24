package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/pgcluster"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/r2"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// copyRepo copies core/cnpg/cluster into a new repository top; the
// repository names g1 there.
func copyRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join("..", "..", "..", "..", filepath.FromSlash(pgcluster.ClusterDir))
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		dst := filepath.Join(root, filepath.FromSlash(pgcluster.ClusterDir), rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	g, err := pgcluster.Current(os.DirFS(root))
	if err != nil || g != 1 {
		t.Fatalf("the repository names %v (%v); these tests are written for g1", g, err)
	}
	return root
}

// bucket is a Bucket in memory.
type bucket struct {
	name string
	objs map[string]int64
}

func newBucket(name string, keys ...string) *bucket {
	b := &bucket{name: name, objs: map[string]int64{}}
	for _, k := range keys {
		b.objs[k] = 1
	}
	return b
}

func (b *bucket) Name() string { return b.name }

func (b *bucket) Prefixes(context.Context) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for k := range b.objs {
		p, _, _ := strings.Cut(k, "/")
		if !seen[p] {
			seen[p] = true
			out = append(out, p+"/")
		}
	}
	return out, nil
}

func (b *bucket) List(_ context.Context, prefix string) ([]r2.Object, error) {
	var out []r2.Object
	for k, n := range b.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, r2.Object{Key: k, Size: n})
		}
	}
	return out, nil
}

func (b *bucket) Delete(_ context.Context, keys []string) error {
	for _, k := range keys {
		delete(b.objs, k)
	}
	return nil
}

type locks []r2.LockRule

func (l locks) Rules(context.Context, string) ([]r2.LockRule, error) { return l, nil }

func lockOn(prefix string, days int64) r2.LockRule {
	r := r2.LockRule{ID: "lock", Enabled: true, Prefix: prefix}
	r.Condition.Type = "Age"
	r.Condition.MaxAgeSeconds = days * 24 * 3600
	return r
}

// cp is a Control Plane: it answers the kubectl commands the Stages run.
type cp struct {
	cluster string // the Cluster's JSON; empty for none
	diffRC  int
	missing string // a name `kubectl get` does not find
	backups string
	runs    []string
}

func (c *cp) Run(_ context.Context, cmd string) (string, error) {
	c.runs = append(c.runs, cmd)
	switch {
	case c.missing != "" && strings.Contains(cmd, "'"+c.missing+"'"):
		return "", fmt.Errorf("NotFound: %s", c.missing)
	case strings.HasPrefix(cmd, "mkdir"):
		return "", nil
	case strings.Contains(cmd, "kubectl diff"):
		return fmt.Sprintf("diff -u -N /tmp/LIVE-1/x /tmp/MERGED-1/x\nkluster-rc=%d", c.diffRC), nil
	case strings.Contains(cmd, "'get' 'clusters.postgresql.cnpg.io'"):
		return c.cluster, nil
	case strings.Contains(cmd, "'get' 'deployments'"):
		return `{"items":[{"metadata":{"name":"cnpg-controller-manager"},"status":{"availableReplicas":1}},
			{"metadata":{"name":"barman-cloud"},"status":{"availableReplicas":1}}]}`, nil
	case strings.Contains(cmd, "'get' 'secrets'"), strings.Contains(cmd, "'get' 'objectstores"):
		return "", nil
	case strings.Contains(cmd, "'get' 'backups"):
		return c.backups, nil
	}
	return "", fmt.Errorf("unexpected command %q", cmd)
}

func (c *cp) WriteFile(context.Context, string, string, os.FileMode) error { return nil }
func (c *cp) ReadFile(context.Context, string) (string, error)             { return "", nil }

func host(c *cp) *stage.Host { return &stage.Host{Name: "cp", Exec: c} }

func names(ss []stage.Stage) string {
	var n []string
	for _, s := range ss {
		n = append(n, s.Name())
	}
	return strings.Join(n, " ")
}

// sourceG1 is the live bucket with generation g1: a base backup and WAL up
// to segment 5 on timeline 1.
func sourceG1() *bucket {
	return newBucket("tosak-pg-backups",
		"tosak-pg-cluster-g1/base/20261020T000000/backup.info",
		"tosak-pg-cluster-g1/wals/0000000100000000/000000010000000000000004.zst",
		"tosak-pg-cluster-g1/wals/0000000100000000/000000010000000000000005.zst",
	)
}

func TestBuildListsTheStagesOfEachRun(t *testing.T) {
	root := copyRepo(t)
	base := "db-preflight db-cluster db-healthy db-archive db-marker db-replication"
	for _, tc := range []struct {
		mode Mode
		live bool
		want string
	}{
		{Fresh, true, base + " db-first-backup"},
		{Fresh, false, base + " db-switchover db-first-backup"},
		{Recover, true, base + " db-first-backup db-generation"},
		{Recover, false, base + " db-switchover db-first-backup"},
	} {
		in := Inputs{Mode: tc.mode, Live: tc.live, Root: root, Archive: newBucket("a"), Source: newBucket("s"), Locks: locks{}}
		p, err := Build(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := names(p.Stages); got != tc.want {
			t.Errorf("%s live=%v: %s, want %s", tc.mode, tc.live, got, tc.want)
		}
		wantArchives := Generation(1)
		if tc.mode == Recover {
			wantArchives = 2
		}
		if p.Archives != wantArchives {
			t.Errorf("%s: archives to %s", tc.mode, p.Archives)
		}
	}
}

func TestBuildRefuses(t *testing.T) {
	root := copyRepo(t)
	for name, in := range map[string]Inputs{
		"no mode":                    {Root: root, Archive: newBucket("a")},
		"no archive":                 {Mode: Fresh, Root: root},
		"a recovery without source":  {Mode: Recover, Root: root, Archive: newBucket("a")},
		"a live recovery, no locks":  {Mode: Recover, Live: true, Root: root, Archive: newBucket("a"), Source: newBucket("s")},
		"a fresh run at a past time": {Mode: Fresh, Root: root, Archive: newBucket("a"), TargetTime: &time.Time{}},
	} {
		if _, err := Build(in); err == nil {
			t.Errorf("%s: built", name)
		}
	}
}

func preflight(t *testing.T, in Inputs) (*Plan, stage.Stage) {
	t.Helper()
	in.Root = copyRepo(t)
	p, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	return p, p.Stages[0]
}

func TestPreflightPassesIntoAnEmptySlot(t *testing.T) {
	_, s := preflight(t, Inputs{Mode: Recover, Live: true, Archive: sourceG1(), Source: sourceG1(),
		Locks: locks{lockOn("tosak-pg-cluster-g2/wals/", 30)}})
	st, err := s.Probe(context.Background(), host(&cp{}))
	if err != nil || !st.Done {
		t.Fatalf("%+v %v", st, err)
	}
	if !strings.Contains(st.Detail, "newest segment 0000000000000005") {
		t.Errorf("detail %q does not name the newest segment", st.Detail)
	}
}

func TestPreflightRefuses(t *testing.T) {
	withG2 := sourceG1()
	withG2.objs["tosak-pg-cluster-g2/wals/0000000200000000/000000020000000000000005.zst"] = 1
	cases := map[string]struct {
		in   Inputs
		cp   *cp
		want string
	}{
		"a fresh run onto a generation that holds objects": {
			Inputs{Mode: Fresh, Archive: sourceG1()}, &cp{}, "holds 3 object(s)"},
		"a recovery into a generation that holds objects": {
			Inputs{Mode: Recover, Archive: withG2, Source: withG2}, &cp{}, "tosak-pg-cluster-g2/ holds 1"},
		"a source newer than the repository": {
			Inputs{Mode: Recover, Archive: newBucket("rehearsal"), Source: withG2}, &cp{}, "newest generation in tosak-pg-backups is g2"},
		"an empty source": {
			Inputs{Mode: Recover, Archive: newBucket("rehearsal"), Source: newBucket("tosak-pg-backups")}, &cp{}, "nothing to recover"},
		"a live recovery without the lock ahead": {
			Inputs{Mode: Recover, Live: true, Archive: sourceG1(), Source: sourceG1(), Locks: locks{lockOn("tosak-pg-cluster-g2/wals/", 7)}},
			&cp{}, "no enabled lock rule"},
		"a missing Secret": {
			Inputs{Mode: Recover, Archive: newBucket("rehearsal"), Source: sourceG1()}, &cp{missing: "r2-source-credentials"}, "kluster services seed"},
		"a Cluster that is not this render": {
			Inputs{Mode: Fresh, Archive: newBucket("rehearsal")}, &cp{cluster: `{"status":{}}`, diffRC: 1}, "only into an empty slot"},
	}
	for name, tc := range cases {
		_, s := preflight(t, tc.in)
		_, err := s.Probe(context.Background(), host(tc.cp))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

func TestPreflightResumesItsOwnRender(t *testing.T) {
	_, s := preflight(t, Inputs{Mode: Fresh, Archive: newBucket("rehearsal")})
	st, err := s.Probe(context.Background(), host(&cp{cluster: `{"status":{}}`, diffRC: 0}))
	if err != nil || !st.Done || !strings.Contains(st.Detail, "resumed") {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestArchiveFollowsTheSource(t *testing.T) {
	newest := walFile{Pos: "0000000000000005"}
	for _, tc := range []struct {
		first   string
		done    bool
		refused bool
	}{
		{"000000020000000000000006", true, false},
		// The last record ran past segment 5: timeline 2 opens in it.
		{"000000020000000000000005", true, false},
		{"000000020000000000000004", false, true},
	} {
		archive := newBucket("rehearsal",
			"tosak-pg-cluster-g2/wals/00000002.history",
			"tosak-pg-cluster-g2/wals/0000000200000000/"+tc.first+".zst")
		p, _ := preflight(t, Inputs{Mode: Recover, Archive: archive, Source: sourceG1()})
		s := p.Stages[3].(*Archive)
		s.sourceNewest = &newest
		st, err := s.Probe(context.Background(), host(&cp{}))
		if st.Done != tc.done || (err != nil) != tc.refused {
			t.Errorf("first %s: %+v %v", tc.first, st, err)
		}
	}
}

func TestArchiveWaitsForTheHistory(t *testing.T) {
	archive := newBucket("rehearsal")
	p, _ := preflight(t, Inputs{Mode: Recover, Archive: archive, Source: sourceG1()})
	st, err := p.Stages[3].Probe(context.Background(), host(&cp{}))
	if err != nil || st.Done || !strings.Contains(st.Detail, "no timeline history") {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestFirstBackupIsProvenInTheBucket(t *testing.T) {
	backups := `{"items":[{"metadata":{"name":"tosak-pg-cluster-first-20261024-120000"},
		"spec":{"cluster":{"name":"tosak-pg-cluster"}},"status":{"phase":"completed","backupId":"20261024T120001"}}]}`
	dir := "tosak-pg-cluster-g1/base/20261024T120001/"
	for _, tc := range []struct {
		keys []string
		done bool
	}{
		{[]string{dir + "backup.info", dir + "data.tar.gz"}, true},
		{[]string{dir + "backup.info"}, false},
	} {
		p, _ := preflight(t, Inputs{Mode: Fresh, Archive: newBucket("rehearsal", tc.keys...)})
		var s stage.Stage
		for _, x := range p.Stages {
			if x.Name() == "db-first-backup" {
				s = x
			}
		}
		st, err := s.Probe(context.Background(), host(&cp{backups: backups}))
		if st.Done != tc.done || (err == nil) != tc.done {
			t.Errorf("%v: %+v %v", tc.keys, st, err)
		}
	}
}

func TestFirstBackupRefusesAFailedOne(t *testing.T) {
	backups := `{"items":[{"metadata":{"name":"tosak-pg-cluster-first-x"},
		"spec":{"cluster":{"name":"tosak-pg-cluster"}},"status":{"phase":"failed","error":"boom"}}]}`
	p, _ := preflight(t, Inputs{Mode: Fresh, Archive: newBucket("rehearsal")})
	_, err := p.Stages[len(p.Stages)-1].Probe(context.Background(), host(&cp{backups: backups}))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("%v", err)
	}
}

func TestGenerationBumpWritesTheNextGeneration(t *testing.T) {
	p, _ := preflight(t, Inputs{Mode: Recover, Live: true, Archive: sourceG1(), Source: sourceG1(), Locks: locks{}})
	s := p.Stages[len(p.Stages)-1]
	if s.Name() != "db-generation" {
		t.Fatalf("last Stage is %s", s.Name())
	}
	ctx, h := context.Background(), host(&cp{})
	if st, _ := s.Probe(ctx, h); st.Done {
		t.Fatal("done before the bump")
	}
	if err := s.Act(ctx, h); err != nil {
		t.Fatal(err)
	}
	st, err := s.Probe(ctx, h)
	if err != nil || !st.Done || !strings.Contains(st.Detail, "commit it") {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestParseWAL(t *testing.T) {
	for key, want := range map[string]walFile{
		"g/wals/0000000200000000/000000020000000A0000001F.zst":     {Timeline: 2, Pos: "0000000A0000001F"},
		"g/wals/0000000100000000/000000010000000000000005.partial": {Timeline: 1, Pos: "0000000000000005", Partial: true},
		"g/wals/00000003.history.zst":                              {Timeline: 3, History: true},
	} {
		got, ok := parseWAL(key)
		want.Key = key
		if !ok || got != want {
			t.Errorf("%s: %+v %v, want %+v", key, got, ok, want)
		}
	}
	for _, key := range []string{"g/wals/000000010000000000000005.00000028.backup", "g/wals/.check-empty-wal-archive", "g/wals/x"} {
		if _, ok := parseWAL(key); ok {
			t.Errorf("%s parsed", key)
		}
	}
}
