package cloud

import (
	"context"
	"strings"
	"testing"
)

// A server type passes only where Hetzner offers it, in stock and not
// deprecated.
func TestCheckServerType(t *testing.T) {
	f := &fakeAPI{serverTypes: `[{"id":1,"name":"cx33","locations":[
	  {"id":1,"name":"fsn1","available":true,"deprecation":null},
	  {"id":3,"name":"hel1","available":false,"deprecation":null},
	  {"id":4,"name":"nbg1","available":true,"deprecation":{"announced":"2026-01-01T00:00:00Z","unavailable_after":"2026-12-01T00:00:00Z"}}]}]`}
	c := newFake(t, f)
	for loc, want := range map[string]string{
		"fsn1": "",
		"hel1": "out of stock in hel1",
		"nbg1": "deprecated in nbg1",
		"ash":  "not offered in ash",
	} {
		err := c.CheckServerType(context.Background(), "cx33", loc)
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: err = %v, want %q", loc, err, want)
		}
	}
	f.serverTypes = "[]"
	if err := c.CheckServerType(context.Background(), "cx99", "hel1"); err == nil || !strings.Contains(err.Error(), "no server type cx99") {
		t.Errorf("unknown type: err = %v", err)
	}
}
