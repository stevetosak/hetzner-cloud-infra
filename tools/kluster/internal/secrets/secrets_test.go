package secrets

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/sops"
)

var ctx = context.Background()

func TestGenerateFromNothing(t *testing.T) {
	f := newFixture(t, nil)
	p := f.apply(f.planner.Generate(ctx))

	if got, want := changes(p, "db/creds"), []string{"+ password", "+ username"}; !slices.Equal(got, want) {
		t.Errorf("db/creds: %q, want %q", got, want)
	}
	if got, want := changes(p, "app/credentials"), []string{"+ DATABASE_URL", "+ DB_PASS", "! API_TOKEN"}; !slices.Equal(got, want) {
		t.Errorf("app/credentials: %q, want %q", got, want)
	}

	db, app := f.values("db/creds"), f.values("app/credentials")
	if len(db["password"]) != catalog.DefaultLength || string(db["username"]) != "tosak" {
		t.Errorf("db/creds: password %d bytes, username set %v", len(db["password"]), string(db["username"]) == "tosak")
	}
	if !bytes.Equal(app["DB_PASS"], db["password"]) {
		t.Error("DB_PASS is not the password")
	}
	if want := "postgresql://tosak:" + string(db["password"]) + "@db:5432/app"; string(app["DATABASE_URL"]) != want {
		t.Error("DATABASE_URL is not the formatted password")
	}
	for _, s := range f.cat.Secrets {
		data, err := os.ReadFile(f.cat.Path(s))
		if err != nil {
			t.Fatal(err)
		}
		if err := sops.CheckEncrypted(data); err != nil {
			t.Errorf("%s: %v", s.File, err)
		}
	}

	// A second run finds nothing to do.
	again, err := f.planner.Generate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed() {
		t.Errorf("second generate changes: %q %q", changes(again, "db/creds"), changes(again, "app/credentials"))
	}
}

func TestRotateRewritesEveryCopy(t *testing.T) {
	f := newFixture(t, nil)
	f.apply(f.planner.Generate(ctx))
	before := f.values("db/creds")["password"]

	ref := catalog.KeyRef{Secret: catalog.Ref{Namespace: "db", Name: "creds"}, Key: "password"}
	p := f.apply(f.planner.Rotate(ctx, []catalog.KeyRef{ref}))
	if got, want := changes(p, "db/creds"), []string{"~ password"}; !slices.Equal(got, want) {
		t.Errorf("db/creds: %q, want %q", got, want)
	}
	if got, want := changes(p, "app/credentials"), []string{"~ DATABASE_URL", "~ DB_PASS", "! API_TOKEN"}; !slices.Equal(got, want) {
		t.Errorf("app/credentials: %q, want %q", got, want)
	}
	after := f.values("db/creds")["password"]
	if bytes.Equal(before, after) || !bytes.Equal(f.values("app/credentials")["DB_PASS"], after) {
		t.Error("the password did not change, or its copy did not follow")
	}

	issued := catalog.KeyRef{Secret: catalog.Ref{Namespace: "app", Name: "credentials"}, Key: "API_TOKEN"}
	if _, err := f.planner.Rotate(ctx, []catalog.KeyRef{issued}); !errors.Is(err, ErrIssuedRotate) {
		t.Errorf("rotate of an issued key: %v", err)
	}
	copyRef := catalog.KeyRef{Secret: issued.Secret, Key: "DB_PASS"}
	if _, err := f.planner.Rotate(ctx, []catalog.KeyRef{copyRef}); err == nil || !strings.Contains(err.Error(), "copy key") {
		t.Errorf("rotate of a copy: %v", err)
	}
}

func TestPullAndSet(t *testing.T) {
	f := newFixture(t, fakeVault{"kluster/app/api_token": marker + "-from-bitwarden"})
	f.apply(f.planner.Generate(ctx))

	p := f.apply(f.planner.Pull(ctx, nil))
	if got, want := changes(p, "app/credentials"), []string{"+ API_TOKEN"}; !slices.Equal(got, want) {
		t.Errorf("pull: %q, want %q", got, want)
	}
	if changes(p, "db/creds") != nil {
		t.Errorf("pull touched db/creds: %q", changes(p, "db/creds"))
	}

	ref := catalog.KeyRef{Secret: catalog.Ref{Namespace: "app", Name: "credentials"}, Key: "API_TOKEN"}
	p = f.apply(f.planner.Set(ctx, ref, []byte(marker+"-typed")))
	if got, want := changes(p, "app/credentials"), []string{"~ API_TOKEN"}; !slices.Equal(got, want) {
		t.Errorf("set: %q, want %q", got, want)
	}
	if string(f.values("app/credentials")["API_TOKEN"]) != marker+"-typed" {
		t.Error("set did not write the value")
	}
	if _, err := f.planner.Set(ctx, ref, nil); err == nil {
		t.Error("set of an empty value passed")
	}
	gen := catalog.KeyRef{Secret: catalog.Ref{Namespace: "db", Name: "creds"}, Key: "password"}
	if _, err := f.planner.Set(ctx, gen, []byte("x")); err == nil {
		t.Error("set of a generated key passed")
	}
	if _, err := NewPlanner(f.store, nil).Pull(ctx, nil); err == nil {
		t.Error("pull without a vault passed")
	}
}

// TestUncatalogedKeyAndShape covers the DB_USER move: a key the catalog no
// longer lists leaves at the next write, and a file with old labels is
// rewritten.
func TestUncatalogedKeyAndShape(t *testing.T) {
	f := newFixture(t, nil)
	f.apply(f.planner.Generate(ctx))
	app := f.secret("app/credentials")
	v := f.values("app/credentials")
	v["DB_USER"] = []byte("tosak")
	if err := f.store.Write(ctx, app, v); err != nil {
		t.Fatal(err)
	}
	db := f.secret("db/creds")
	stale := *db
	stale.Labels = nil
	if err := f.store.Write(ctx, &stale, f.values("db/creds")); err != nil {
		t.Fatal(err)
	}

	p, err := f.planner.Generate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := changes(p, "app/credentials"), []string{"- DB_USER", "! API_TOKEN"}; !slices.Equal(got, want) {
		t.Errorf("app/credentials: %q, want %q", got, want)
	}
	if !p.Files[slices.IndexFunc(p.Files, func(fp *FilePlan) bool { return fp.Secret == db })].Reshape {
		t.Error("db/creds with no labels is not reshaped")
	}
	if _, _, err := Manifest(ctx, f.store, app); err == nil {
		t.Error("Manifest of a file with an uncatalogued key passed")
	}
	f.apply(p, nil)
	if _, ok := f.values("app/credentials")["DB_USER"]; ok {
		t.Error("DB_USER is still there")
	}
	if _, _, mismatch, _ := f.store.Read(ctx, db); mismatch {
		t.Error("db/creds still has the wrong labels")
	}
}

func TestReadRefusesAnotherSecret(t *testing.T) {
	f := newFixture(t, nil)
	db := f.secret("db/creds")
	other := *db
	other.Ref = catalog.Ref{Namespace: "db", Name: "other"}
	if err := f.store.Write(ctx, &other, Values{"password": []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.store.Read(ctx, db); err == nil || !strings.Contains(err.Error(), "db/other") {
		t.Errorf("Read of a file holding another Secret: %v", err)
	}
}

func TestStoreAsksForTheKeyOnce(t *testing.T) {
	f := newFixture(t, nil)
	asked := 0
	f.store = NewStore(f.cat, f.cipher, func(context.Context) (string, error) { asked++; return fakeKey, nil })
	f.planner.store = f.store
	if _, err := f.planner.Generate(ctx); err != nil {
		t.Fatal(err)
	}
	if asked != 0 {
		t.Errorf("generate with no files asked for the key %d times", asked)
	}
	f.apply(f.planner.Generate(ctx))
	f.apply(f.planner.Generate(ctx))
	if asked != 1 {
		t.Errorf("asked for the key %d times, want 1", asked)
	}
}

func TestList(t *testing.T) {
	vault := fakeVault{"kluster/app/api_token": "bw"}
	f := newFixture(t, vault)
	f.apply(f.planner.Generate(ctx))

	state := func(rows []Row, key string) State {
		for _, r := range rows {
			if r.Key == key {
				return r.State
			}
		}
		return ""
	}
	rows, err := List(ctx, f.store, vault)
	if err != nil {
		t.Fatal(err)
	}
	if state(rows, "API_TOKEN") != Missing || state(rows, "password") != Present || state(rows, "DB_PASS") != Present {
		t.Errorf("rows %v", rows)
	}

	ref := catalog.KeyRef{Secret: catalog.Ref{Namespace: "app", Name: "credentials"}, Key: "API_TOKEN"}
	f.apply(f.planner.Set(ctx, ref, []byte("typed")))
	app := f.secret("app/credentials")
	v := f.values("app/credentials")
	v["DB_PASS"] = []byte("old")
	if err := f.store.Write(ctx, app, v); err != nil {
		t.Fatal(err)
	}
	rows, err = List(ctx, f.store, vault)
	if err != nil {
		t.Fatal(err)
	}
	if state(rows, "API_TOKEN") != StaleIssued || state(rows, "DB_PASS") != StaleCopy {
		t.Errorf("rows %v", rows)
	}
	rows, _ = List(ctx, f.store, nil)
	if state(rows, "API_TOKEN") != NotComparedBW {
		t.Errorf("without a vault: %v", state(rows, "API_TOKEN"))
	}
	rows, err = List(ctx, f.store, fakeVault{})
	if err != nil {
		t.Fatalf("a missing Bitwarden item stopped list: %v", err)
	}
	if state(rows, "API_TOKEN") != NotInBitwarden {
		t.Errorf("missing item: %v", state(rows, "API_TOKEN"))
	}
}

func TestDiffAndApply(t *testing.T) {
	f := newFixture(t, fakeVault{"kluster/app/api_token": "bw"})
	f.apply(f.planner.Generate(ctx))
	f.apply(f.planner.Pull(ctx, nil))
	db, app := f.secret("db/creds"), f.secret("app/credentials")

	cl := &fakeCluster{live: map[catalog.Ref][]byte{}}
	// The live app Secret has an old DB_PASS, lacks DATABASE_URL and still
	// holds DB_USER.
	old := f.values("app/credentials")
	old["DB_PASS"] = []byte("old")
	old["DB_USER"] = []byte("tosak")
	delete(old, "DATABASE_URL")
	extended := *app
	extended.Keys = map[string]*catalog.Key{"DB_USER": {Kind: catalog.Literal}}
	for k, v := range app.Keys {
		extended.Keys[k] = v
	}
	live, err := render(&extended, old)
	if err != nil {
		t.Fatal(err)
	}
	cl.live[app.Ref] = live

	diffs, err := Diff(ctx, f.store, cl, []*catalog.Secret{db, app})
	if err != nil {
		t.Fatal(err)
	}
	if !diffs[0].Absent {
		t.Error("db/creds is not absent")
	}
	var got []string
	for _, c := range diffs[1].Changes {
		got = append(got, string(c.Op)+" "+c.Key)
	}
	if want := []string{"+ DATABASE_URL", "~ DB_PASS", "- DB_USER"}; !slices.Equal(got, want) {
		t.Errorf("app diff %q, want %q", got, want)
	}

	if err := Apply(ctx, f.store, cl, diffs); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cl.applied, []catalog.Ref{db.Ref, app.Ref}) {
		t.Errorf("applied %v", cl.applied)
	}
	diffs, _ = Diff(ctx, f.store, cl, []*catalog.Secret{db, app})
	var out bytes.Buffer
	drift, err := PrintDiff(&out, diffs)
	if err != nil || drift {
		t.Errorf("after apply: drift %v, %v\n%s", drift, err, out.String())
	}
}

func TestApplyRefusesIncompleteSecret(t *testing.T) {
	f := newFixture(t, nil)
	f.apply(f.planner.Generate(ctx))
	_, _, err := Manifest(ctx, f.store, f.secret("app/credentials"))
	if err == nil || !strings.Contains(err.Error(), "API_TOKEN") {
		t.Errorf("Manifest of a Secret without API_TOKEN: %v", err)
	}
	f2 := newFixture(t, nil)
	if _, _, err := Manifest(ctx, f2.store, f2.secret("db/creds")); err == nil {
		t.Error("Manifest of a file that does not exist passed")
	}
}

// TestNoValueInOutput runs every printer over a plan, a list and a diff and
// proves none holds a value.
func TestNoValueInOutput(t *testing.T) {
	vault := fakeVault{"kluster/app/api_token": marker + "-bw"}
	f := newFixture(t, vault)
	var out bytes.Buffer
	p, err := f.planner.Generate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p.Print(&out)
	f.apply(p, nil)
	p, _ = f.planner.Pull(ctx, nil)
	p.Print(&out)
	f.apply(p, nil)
	rows, _ := List(ctx, f.store, vault)
	PrintList(&out, rows)
	cl := &fakeCluster{live: map[catalog.Ref][]byte{}}
	diffs, _ := Diff(ctx, f.store, cl, f.cat.Secrets)
	PrintDiff(&out, diffs)

	if strings.Contains(out.String(), marker) || strings.Contains(out.String(), "tosak") {
		t.Errorf("output holds a value:\n%s", out.String())
	}
	for _, want := range []string{"+ password", "generated, 40 characters", "copy of db/creds.password", "Bitwarden, kluster/app field api_token", "SECRET ", "not on the cluster"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestCheck(t *testing.T) {
	f := newFixture(t, nil)
	f.apply(f.planner.Generate(ctx))
	good, _ := os.ReadFile(f.cat.Path(f.secret("db/creds")))
	files := []File{
		{"core/db/creds.enc.yaml", good},
		{"core/db/stray.enc.yaml", good},
		{"projects/app/web/manifests/credentials.enc.yaml", []byte("data:\n  API_TOKEN: " + marker + "\n")},
		{"README.md", []byte("not a secret")},
	}
	errs := Check(f.cat, files)
	var msgs []string
	for _, e := range errs {
		msgs = append(msgs, e.Error())
	}
	all := strings.Join(msgs, "\n")
	if len(errs) != 2 || !strings.Contains(all, "stray.enc.yaml: not listed") || !strings.Contains(all, "credentials.enc.yaml: ") {
		t.Errorf("errors:\n%s", all)
	}
	if strings.Contains(all, marker) {
		t.Errorf("an error quotes a value:\n%s", all)
	}
	absent := Absent(f.cat, func(rel string) bool { return rel == "core/db/creds.enc.yaml" })
	if !slices.Equal(absent, []string{"projects/app/web/manifests/credentials.enc.yaml"}) {
		t.Errorf("absent %q", absent)
	}
}

func TestWriteIsAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a", "x.enc.yaml")
	if err := writeAtomic(p, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(p, []byte("two")); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Errorf("leftover files: %v", entries)
	}
	if b, _ := os.ReadFile(p); string(b) != "two" {
		t.Errorf("content %q", b)
	}
}

func TestRandom(t *testing.T) {
	a, err := Random(64)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Random(64)
	if len(a) != 64 || bytes.Equal(a, b) {
		t.Error("Random repeats or has the wrong length")
	}
	for _, c := range a {
		if !strings.ContainsRune(alphabet, rune(c)) {
			t.Errorf("character %q is not alphanumeric", c)
		}
	}
	if _, err := Random(0); err == nil {
		t.Error("Random(0) passed")
	}
}
