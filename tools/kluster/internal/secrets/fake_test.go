package secrets

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
)

// testCatalog is a small catalog with every kind: a generated source, a
// literal, a formatted copy and an issued key.
const testCatalog = `secrets:
  db/creds:
    file: core/db/creds.enc.yaml
    type: kubernetes.io/basic-auth
    labels:
      cnpg.io/reload: "true"
    keys:
      username:
        kind: literal
        value: tosak
      password:
        kind: generated
  app/credentials:
    file: projects/app/web/manifests/credentials.enc.yaml
    keys:
      DATABASE_URL:
        kind: copy
        from: db/creds.password
        format: "postgresql://tosak:{value}@db:5432/app"
      DB_PASS:
        kind: copy
        from: db/creds.password
      API_TOKEN:
        kind: issued
        item: kluster/app
        field: api_token
`

// fakeCipher stands in for sops. It keeps the manifest in the clear except
// data and stringData values, which become ENC tokens holding the value in
// base64, and adds the sops metadata, so sops.CheckEncrypted accepts it.
type fakeCipher struct{ decrypts int }

const fakeKey = "AGE-SECRET-KEY-FAKE"

func (f *fakeCipher) Encrypt(_ context.Context, plain []byte, _ string) ([]byte, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(plain, &doc); err != nil {
		return nil, err
	}
	for _, sec := range []string{"data", "stringData"} {
		m, _ := doc[sec].(map[string]any)
		for k, v := range m {
			m[k] = "ENC[AES256_GCM,data:" + base64.StdEncoding.EncodeToString([]byte(fmt.Sprint(v))) + ",iv:aXY=,tag:dGFn,type:str]"
		}
	}
	doc["sops"] = map[string]any{
		"age": []any{map[string]any{"recipient": "age1fake"}},
		"mac": "ENC[AES256_GCM,data:bWFj,iv:aXY=,tag:dGFn,type:str]",
	}
	return yaml.Marshal(doc)
}

func (f *fakeCipher) Decrypt(_ context.Context, cipher []byte, _ string, key string) ([]byte, error) {
	if key != fakeKey {
		return nil, errors.New("wrong key")
	}
	f.decrypts++
	var doc map[string]any
	if err := yaml.Unmarshal(cipher, &doc); err != nil {
		return nil, err
	}
	delete(doc, "sops")
	for _, sec := range []string{"data", "stringData"} {
		m, _ := doc[sec].(map[string]any)
		for k, v := range m {
			s := strings.TrimPrefix(fmt.Sprint(v), "ENC[AES256_GCM,data:")
			s = s[:strings.Index(s, ",")]
			b, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				return nil, err
			}
			m[k] = string(b)
		}
	}
	return yaml.Marshal(doc)
}

// fakeVault answers issued keys by item and field.
type fakeVault map[string]string

func (v fakeVault) Issued(_ context.Context, k *catalog.Key) ([]byte, error) {
	s, ok := v[k.Item+"/"+k.Field]
	if !ok {
		return nil, fmt.Errorf("no item %s", k.Item)
	}
	return []byte(s), nil
}

// fakeCluster holds live Secrets as YAML and records applies.
type fakeCluster struct {
	live    map[catalog.Ref][]byte
	applied []catalog.Ref
}

func (c *fakeCluster) Secret(_ context.Context, ref catalog.Ref) ([]byte, error) {
	return c.live[ref], nil
}

func (c *fakeCluster) Apply(_ context.Context, m []byte) error {
	sh, _, err := parse(m)
	if err != nil {
		return err
	}
	c.live[sh.ref] = m
	c.applied = append(c.applied, sh.ref)
	return nil
}

// marker is in every generated test value, so a test can prove no output
// holds one.
const marker = "SECRETVALUE"

type fixture struct {
	t       *testing.T
	root    string
	cat     *catalog.Catalog
	cipher  *fakeCipher
	store   *Store
	planner *Planner
	n       int
}

func newFixture(t *testing.T, vault Vault) *fixture {
	t.Helper()
	root := t.TempDir()
	cat, err := catalog.Parse([]byte(testCatalog), root)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, root: root, cat: cat, cipher: &fakeCipher{}}
	f.store = NewStore(cat, f.cipher, func(context.Context) (string, error) { return fakeKey, nil })
	f.planner = NewPlanner(f.store, vault)
	f.planner.random = func(n int) ([]byte, error) {
		f.n++
		v := fmt.Sprintf("%s%d", marker, f.n)
		return []byte(v + strings.Repeat("x", n-len(v))), nil
	}
	return f
}

func (f *fixture) secret(ref string) *catalog.Secret {
	r, _ := catalog.ParseRef(ref)
	s, ok := f.cat.Secret(r)
	if !ok {
		f.t.Fatalf("no %s", ref)
	}
	return s
}

func (f *fixture) values(ref string) Values {
	f.t.Helper()
	v, _, _, err := f.store.Read(context.Background(), f.secret(ref))
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}

func (f *fixture) apply(p *Plan, err error) *Plan {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.planner.Write(context.Background(), p); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fixture) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(f.root, rel))
	return err == nil
}

// changes renders a FilePlan's changes as "op key" strings.
func changes(p *Plan, ref string) []string {
	var out []string
	for _, fp := range p.Files {
		if fp.Secret.Ref.String() != ref {
			continue
		}
		for _, c := range fp.Changes {
			out = append(out, string(c.Op)+" "+c.Key)
		}
		for _, m := range fp.Missing {
			out = append(out, "! "+m)
		}
	}
	return out
}
