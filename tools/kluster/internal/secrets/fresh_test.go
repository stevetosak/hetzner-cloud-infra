package secrets

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
)

const freshCatalog = `
secrets:
  redis/redis:
    file: core/redis/redis.enc.yaml
    keys:
      password: {kind: generated}
  pg-cluster/db-credentials:
    file: core/cnpg/db-credentials.enc.yaml
    type: kubernetes.io/basic-auth
    labels: {cnpg.io/reload: "true"}
    keys:
      username: {kind: literal, value: tosak}
      password: {kind: generated, length: 64}
  redis/url:
    file: core/redis/url.enc.yaml
    keys:
      URL: {kind: copy, from: redis/redis.password, format: "redis://:{value}@redis"}
  cert-manager/token:
    file: core/cert-manager/token.enc.yaml
    rehearsal: omit
    keys:
      api-token: {kind: issued, item: kluster/cloudflare-dns, field: api_token}
  pg-cluster/r2:
    file: core/cnpg/r2.enc.yaml
    keys:
      KEY: {kind: issued, item: kluster/r2-live, field: access_key_id, rehearsalItem: kluster/r2-rehearsal}
`

// rehearsalVault holds only the rehearsal item: a read of the live one fails.
var rehearsalVault = fakeVault{"kluster/r2-rehearsal/access_key_id": "rehearsal-key"}

func TestFreshMakesEveryKind(t *testing.T) {
	cat, err := catalog.Parse([]byte(freshCatalog), "/repo")
	if err != nil {
		t.Fatal(err)
	}
	var made []*catalog.Secret
	for _, s := range cat.Secrets {
		if !s.OmittedInRehearsal() {
			made = append(made, s)
		}
	}
	first, err := Fresh(context.Background(), made, rehearsalVault)
	if err != nil {
		t.Fatal(err)
	}
	values := func(ns, name string) (shape, Values) {
		sh, v, err := parse(first[catalog.Ref{Namespace: ns, Name: name}])
		if err != nil {
			t.Fatal(err)
		}
		return sh, v
	}
	sh, db := values("pg-cluster", "db-credentials")
	if sh.typ != "kubernetes.io/basic-auth" || sh.labels["cnpg.io/reload"] != "true" {
		t.Errorf("shape not from the catalog: %+v", sh)
	}
	if string(db["username"]) != "tosak" || len(db["password"]) != 64 {
		t.Errorf("username %q, password length %d", db["username"], len(db["password"]))
	}
	_, redis := values("redis", "redis")
	_, url := values("redis", "url")
	if string(url["URL"]) != "redis://:"+string(redis["password"])+"@redis" {
		t.Error("the copy does not hold its source's fresh value")
	}

	again, err := Fresh(context.Background(), made, rehearsalVault)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(again[catalog.Ref{Namespace: "redis", Name: "redis"}], first[catalog.Ref{Namespace: "redis", Name: "redis"}]) {
		t.Error("two runs made the same value")
	}
	if _, r2 := values("pg-cluster", "r2"); string(r2["KEY"]) != "rehearsal-key" {
		t.Errorf("issued key with a rehearsalItem = %q, want the rehearsal item's value", r2["KEY"])
	}
	urlSecret, _ := cat.Secret(catalog.Ref{Namespace: "redis", Name: "url"})
	ok, err := HasKeys(first[urlSecret.Ref], urlSecret)
	if err != nil || !ok {
		t.Errorf("HasKeys = %t, %v", ok, err)
	}
}

func TestFreshRefuses(t *testing.T) {
	cat, err := catalog.Parse([]byte(freshCatalog), "/repo")
	if err != nil {
		t.Fatal(err)
	}
	token, _ := cat.Secret(catalog.Ref{Namespace: "cert-manager", Name: "token"})
	if _, err := Fresh(context.Background(), []*catalog.Secret{token}, rehearsalVault); err == nil || !strings.Contains(err.Error(), "no rehearsal value") {
		t.Errorf("issued key: %v", err)
	}
	url, _ := cat.Secret(catalog.Ref{Namespace: "redis", Name: "url"})
	if _, err := Fresh(context.Background(), []*catalog.Secret{url}, rehearsalVault); err == nil || !strings.Contains(err.Error(), "does not make") {
		t.Errorf("copy without its source: %v", err)
	}
}
