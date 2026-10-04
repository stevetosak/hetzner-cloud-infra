package intent

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	tfjson "github.com/hashicorp/terraform-json"
)

func plan(changes map[string]tfjson.Actions) *tfjson.Plan {
	p := &tfjson.Plan{}
	for addr, a := range changes {
		p.ResourceChanges = append(p.ResourceChanges, &tfjson.ResourceChange{
			Address: addr,
			Change:  &tfjson.Change{Actions: a},
		})
	}
	return p
}

var (
	create  = tfjson.Actions{tfjson.ActionCreate}
	update  = tfjson.Actions{tfjson.ActionUpdate}
	del     = tfjson.Actions{tfjson.ActionDelete}
	noop    = tfjson.Actions{tfjson.ActionNoop}
	replace = tfjson.Actions{tfjson.ActionDelete, tfjson.ActionCreate}
)

func TestChangesClassifiesAndDropsNoOps(t *testing.T) {
	got, err := Changes(plan(map[string]tfjson.Actions{
		"b":    replace,
		"a":    create,
		"keep": noop,
		"c":    {tfjson.ActionCreate, tfjson.ActionDelete},
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := []Change{{"a", Create}, {"b", Replace}, {"c", Replace}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Error(diff)
	}
}

func TestChangesRefusesUnknownActions(t *testing.T) {
	if _, err := Changes(plan(map[string]tfjson.Actions{"x": {"forget"}})); err == nil {
		t.Fatal("an unknown action set must not pass as a no-op")
	}
}

// The 2026-09-13 loss in one test: "add one worker" planned as four
// replacements and a create. The old Apply could not tell the two apart.
func TestNodeAddIntentCatchesTheIncident(t *testing.T) {
	addOne := Intent{Expectations: []Expectation{
		{Address: `hcloud_server.workers["k8swk4"]`, Actions: []Action{Create}, Required: true},
	}}
	changes, _ := Changes(plan(map[string]tfjson.Actions{
		`hcloud_server.workers["k8swk1"]`: replace,
		`hcloud_server.workers["k8swk2"]`: replace,
		`hcloud_server.workers["k8swk3"]`: replace,
		`hcloud_server.workers["k8swk4"]`: create,
	}))
	got := addOne.Check(changes)
	if len(got) != 3 {
		t.Fatalf("want 3 unexpected replacements, got %v", got)
	}
	for _, m := range got {
		if m.Planned != Replace {
			t.Errorf("want a replace mismatch, got %s", m)
		}
	}
}

func TestCheck(t *testing.T) {
	fw := Intent{Expectations: []Expectation{
		{Address: "hcloud_firewall.worker", Actions: []Action{Update}, Required: true},
	}}
	tests := []struct {
		name    string
		intent  Intent
		changes map[string]tfjson.Actions
		want    []Mismatch
	}{
		{"exact match", fw, map[string]tfjson.Actions{"hcloud_firewall.worker": update}, nil},
		{"required missing", fw, nil, []Mismatch{
			{Address: "hcloud_firewall.worker", Allowed: []Action{Update}},
		}},
		{"wrong action on an expected address", fw, map[string]tfjson.Actions{"hcloud_firewall.worker": del}, []Mismatch{
			{Address: "hcloud_firewall.worker", Planned: Delete, Allowed: []Action{Update}},
			{Address: "hcloud_firewall.worker", Allowed: []Action{Update}},
		}},
		{"extra change", fw, map[string]tfjson.Actions{
			"hcloud_firewall.worker": update,
			"hcloud_firewall.cp":     update,
		}, []Mismatch{{Address: "hcloud_firewall.cp", Planned: Update}}},
		{"clean intent, clean plan", Intent{}, map[string]tfjson.Actions{"x": noop}, nil},
		{"clean intent, dirty plan", Intent{}, map[string]tfjson.Actions{"x": create}, []Mismatch{
			{Address: "x", Planned: Create},
		}},
		{"glob over brackets", Intent{Expectations: []Expectation{{Address: `hcloud_server.workers["k8swk*"]`, Actions: []Action{Create}}}},
			map[string]tfjson.Actions{`hcloud_server.workers["k8swk4"]`: create, `hcloud_server.control_plane`: create},
			[]Mismatch{{Address: "hcloud_server.control_plane", Planned: Create}}},
		{"glob, creates only", Intent{Expectations: []Expectation{{Address: "*", Actions: []Action{Create}}}},
			map[string]tfjson.Actions{"a": create, "b": replace},
			[]Mismatch{{Address: "b", Planned: Replace, Allowed: []Action{Create}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changes, err := Changes(plan(tt.changes))
			if err != nil {
				t.Fatal(err)
			}
			got := tt.intent.Check(changes)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Error(diff)
			}
		})
	}
}
