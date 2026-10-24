package r2

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fake is a Bucket in memory.
type fake struct {
	name string
	keys map[string]bool
}

func (f *fake) Name() string { return f.name }
func (f *fake) Prefixes(context.Context) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for k := range f.keys {
		if top, _, ok := strings.Cut(k, "/"); ok && !seen[top] {
			seen[top] = true
			out = append(out, top+"/")
		}
	}
	return out, nil
}
func (f *fake) List(_ context.Context, prefix string) ([]Object, error) {
	var out []Object
	for k := range f.keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, Object{Key: k})
		}
	}
	return out, nil
}
func (f *fake) Delete(_ context.Context, keys []string) error {
	for _, k := range keys {
		delete(f.keys, k)
	}
	return nil
}

func TestEmptyRefusesAnotherBucket(t *testing.T) {
	b := &fake{name: "tosak-pg-backups", keys: map[string]bool{"tosak-pg-cluster-g1/base/x": true}}
	if _, err := Empty(context.Background(), b, "tosak-pg-backups-rehearsal"); err == nil {
		t.Fatal("emptied the live bucket")
	}
	if len(b.keys) != 1 {
		t.Fatal("deleted from the live bucket")
	}
	r := &fake{name: "tosak-pg-backups-rehearsal", keys: map[string]bool{"a/1": true, "b/2": true}}
	n, err := Empty(context.Background(), r, "tosak-pg-backups-rehearsal")
	if err != nil || n != 2 || len(r.keys) != 0 {
		t.Fatalf("Empty = %d, %v; left %v", n, err, r.keys)
	}
}

func TestLockRules(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/acc/r2/buckets/tosak-pg-backups/lock" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`))
			return
		}
		w.Write([]byte(`{"success":true,"result":{"rules":[
			{"id":"30d-lock","enabled":true,"prefix":"tosak-pg-cluster-g1/wals/","condition":{"type":"Age","maxAgeSeconds":2592000}},
			{"id":"short","enabled":true,"prefix":"tosak-pg-cluster-g3/wals/","condition":{"type":"Age","maxAgeSeconds":86400}},
			{"id":"off","enabled":false,"prefix":"tosak-pg-cluster-g4/wals/","condition":{"type":"Indefinite"}}]}}`))
	}))
	defer srv.Close()
	l := Locks{Account: "acc", Token: "tok", Base: srv.URL}
	rules, err := l.Rules(context.Background(), "tosak-pg-backups")
	if err != nil {
		t.Fatal(err)
	}
	month := 30 * 24 * time.Hour
	for prefix, want := range map[string]bool{
		"tosak-pg-cluster-g1/wals/": true,
		"tosak-pg-cluster-g2/wals/": false, // no rule
		"tosak-pg-cluster-g3/wals/": false, // too short
		"tosak-pg-cluster-g4/wals/": false, // disabled
		"tosak-pg-cluster-g1/base/": false, // a prefix is literal
	} {
		if _, ok := Locked(rules, prefix, month); ok != want {
			t.Errorf("Locked(%s) = %t, want %t", prefix, ok, want)
		}
	}
	if _, err := (Locks{Account: "acc", Token: "bad", Base: srv.URL}).Rules(context.Background(), "tosak-pg-backups"); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("a refused token: %v", err)
	}
}
