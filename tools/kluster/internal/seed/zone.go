package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// Record is one A record of the zone.
type Record struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
}

// ZoneReader reads the zone's A records. kluster writes no record: DNS
// writes are a later Plan item (external-dns or a zone-write Stage).
type ZoneReader interface {
	ARecords(ctx context.Context) ([]Record, error)
}

// Zone is done when every in-scope host has an A record and each points at
// the load balancer. It never writes: when a record is wrong it names the
// change and fails. A rehearsal's load balancer is never the live one, so
// there this Stage always refuses, by design (ADR 0010, Amendment
// 2026-10-22): that refusal is the mismatch report run for real.
type Zone struct {
	Reader ZoneReader
	// Hosts are the hostnames of the routes the environment syncs.
	Hosts []string
	// Address is the load balancer's address, from the lb-address Stage.
	Address *string

	wrong []string
}

func (*Zone) Name() string    { return "zone" }
func (*Zone) Runbook() string { return Runbook + "#zone" }

func (z *Zone) Probe(ctx context.Context, _ *stage.Host) (stage.Status, error) {
	if *z.Address == "" {
		return stage.Status{Detail: "no load balancer address to compare with yet"}, nil
	}
	recs, err := z.Reader.ARecords(ctx)
	if err != nil {
		return stage.Status{}, err
	}
	z.wrong = Mismatches(recs, z.Hosts, *z.Address)
	if len(z.wrong) == 0 {
		return stage.Status{Done: true, Detail: fmt.Sprintf("%d host(s) point at %s", len(z.Hosts), *z.Address)}, nil
	}
	return stage.Status{Detail: fmt.Sprintf("%d of %d host(s) do not point at %s", len(z.wrong), len(z.Hosts), *z.Address)}, nil
}

// Mismatches names each host whose A records are not exactly one record
// holding addr.
func Mismatches(recs []Record, hosts []string, addr string) []string {
	var out []string
	for _, h := range hosts {
		var got []string
		for _, r := range recs {
			if r.Name == h {
				got = append(got, r.Content)
			}
		}
		switch {
		case len(got) == 0:
			out = append(out, h+": no A record; add one for "+addr)
		case len(got) > 1 || got[0] != addr:
			out = append(out, h+": A "+strings.Join(got, ", ")+"; change it to "+addr)
		}
	}
	return out
}

func (z *Zone) Preview(context.Context, *stage.Host) (string, error) {
	if len(z.wrong) == 0 {
		return "compare the A records of " + strings.Join(z.Hosts, ", ") + " with the load balancer\n", nil
	}
	return "change by hand in Cloudflare (kluster writes no record):\n  " + strings.Join(z.wrong, "\n  ") + "\n", nil
}

func (z *Zone) Act(context.Context, *stage.Host) error {
	return fmt.Errorf("the zone does not point at the load balancer: change the records above by hand, then run the seed again")
}

// Cloudflare reads a zone through the API with a read-only token
// (Zone:Read and DNS:Read on that zone). The token is account-owned, so it
// is never verified at /user/tokens/verify.
type Cloudflare struct {
	Zone  string
	Token string
	// BaseURL is the API's; a test points it at a fake.
	BaseURL string
	Client  *http.Client
}

// CloudflareAPI is the API's base URL.
const CloudflareAPI = "https://api.cloudflare.com/client/v4"

func (c *Cloudflare) get(ctx context.Context, path string, q url.Values, into any) (resultInfo, error) {
	base := c.BaseURL
	if base == "" {
		base = CloudflareAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path+"?"+q.Encode(), nil)
	if err != nil {
		return resultInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	cl := c.Client
	if cl == nil {
		cl = http.DefaultClient
	}
	resp, err := cl.Do(req)
	if err != nil {
		return resultInfo{}, fmt.Errorf("cloudflare %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resultInfo{}, err
	}
	var env struct {
		Success    bool            `json:"success"`
		Errors     []apiError      `json:"errors"`
		Result     json.RawMessage `json:"result"`
		ResultInfo resultInfo      `json:"result_info"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return resultInfo{}, fmt.Errorf("cloudflare %s: HTTP %d, not JSON", path, resp.StatusCode)
	}
	if !env.Success {
		return resultInfo{}, fmt.Errorf("cloudflare %s: HTTP %d: %v", path, resp.StatusCode, env.Errors)
	}
	return env.ResultInfo, json.Unmarshal(env.Result, into)
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e apiError) String() string { return fmt.Sprintf("%d %s", e.Code, e.Message) }

type resultInfo struct {
	Page       int `json:"page"`
	TotalPages int `json:"total_pages"`
}

// ARecords reads every A record of the zone, page by page.
func (c *Cloudflare) ARecords(ctx context.Context) ([]Record, error) {
	var zones []struct {
		ID string `json:"id"`
	}
	if _, err := c.get(ctx, "/zones", url.Values{"name": {c.Zone}}, &zones); err != nil {
		return nil, err
	}
	if len(zones) != 1 {
		return nil, fmt.Errorf("cloudflare: %d zones named %s; the token must see exactly one", len(zones), c.Zone)
	}
	var out []Record
	for page := 1; ; page++ {
		var recs []Record
		info, err := c.get(ctx, "/zones/"+zones[0].ID+"/dns_records",
			url.Values{"type": {"A"}, "per_page": {"100"}, "page": {fmt.Sprint(page)}}, &recs)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
		if page >= info.TotalPages {
			break
		}
	}
	slices.SortFunc(out, func(a, b Record) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}
