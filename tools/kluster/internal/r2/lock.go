package r2

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// APIBase is the Cloudflare API.
const APIBase = "https://api.cloudflare.com/client/v4"

// LockRule is one bucket lock rule. kluster only reads them: making one needs
// R2 configuration write, which no kluster token holds (Amendment
// 2026-10-24).
type LockRule struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Prefix  string `json:"prefix"`
	// Condition is what keeps an object locked. Type Age holds it for
	// MaxAgeSeconds after upload; Indefinite for ever; Date until a date.
	Condition struct {
		Type          string `json:"type"`
		MaxAgeSeconds int64  `json:"maxAgeSeconds"`
	} `json:"condition"`
}

// Holds reports whether the rule locks every object under prefix for at
// least min.
func (r LockRule) Holds(prefix string, min time.Duration) bool {
	if !r.Enabled || len(prefix) < len(r.Prefix) || prefix[:len(r.Prefix)] != r.Prefix {
		return false
	}
	switch r.Condition.Type {
	case "Indefinite":
		return true
	case "Age":
		return time.Duration(r.Condition.MaxAgeSeconds)*time.Second >= min
	}
	return false
}

// Locks reads bucket lock rules with a token that holds R2 Storage Read on
// the account.
type Locks struct {
	Account string
	Token   string
	// Client is http.DefaultClient unless a test replaces it; Base is
	// APIBase.
	Client *http.Client
	Base   string
}

// Rules returns the bucket's lock rules.
func (l Locks) Rules(ctx context.Context, bucket string) ([]LockRule, error) {
	base, c := l.Base, l.Client
	if base == "" {
		base = APIBase
	}
	if c == nil {
		c = http.DefaultClient
	}
	u := fmt.Sprintf("%s/accounts/%s/r2/buckets/%s/lock", base, url.PathEscape(l.Account), url.PathEscape(bucket))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+l.Token)
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reading the lock rules of %s: %w", bucket, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var r struct {
		Success bool `json:"success"`
		Errors  []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
		Result struct {
			Rules []LockRule `json:"rules"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("reading the lock rules of %s: HTTP %d, not JSON", bucket, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || !r.Success {
		return nil, fmt.Errorf("reading the lock rules of %s: HTTP %d: %v", bucket, resp.StatusCode, r.Errors)
	}
	return r.Result.Rules, nil
}

// Locked returns the first rule that holds prefix for min, if any.
func Locked(rules []LockRule, prefix string, min time.Duration) (LockRule, bool) {
	for _, r := range rules {
		if r.Holds(prefix, min) {
			return r, true
		}
	}
	return LockRule{}, false
}
