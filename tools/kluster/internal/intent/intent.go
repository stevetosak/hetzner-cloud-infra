// Package intent checks a Terraform plan against what a command says it will
// do (ADR 0004, Intent Assertion). It reads the plan JSON only, so "create one
// server" and "destroy four, create five" can never look alike.
package intent

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"
)

// Action is the kind of one planned resource change.
type Action string

const (
	Create  Action = "create"
	Update  Action = "update"
	Delete  Action = "delete"
	Replace Action = "replace"
)

// Change is one resource the plan would change. No-op and read changes are
// not Changes.
type Change struct {
	Address string
	Action  Action
}

// Expectation allows changes to the resources whose address matches Address.
// `*` matches any run of characters and nothing else is special, so
// `hcloud_server.workers["k8swk*"]` means what it says: Terraform addresses
// hold brackets, which path.Match would read as a character class.
type Expectation struct {
	Address string
	Actions []Action
	// Required means the plan must hold at least one matching change.
	Required bool
}

// Intent is what a command declares its plan contains. A plan change that no
// Expectation allows is unexpected; so is a Required Expectation that no
// change meets. An Intent with no Expectations expects a clean plan.
type Intent struct {
	Description  string
	Expectations []Expectation
}

// Mismatch is one way the plan and the Intent disagree.
type Mismatch struct {
	Address string
	Planned Action // empty when a required change is missing
	Allowed []Action
}

func (m Mismatch) String() string {
	if m.Planned == "" {
		return fmt.Sprintf("missing: %s, expected one of %v", m.Address, m.Allowed)
	}
	if m.Allowed == nil {
		return fmt.Sprintf("unexpected: %s %s, no change expected", m.Planned, m.Address)
	}
	return fmt.Sprintf("unexpected: %s %s, expected one of %v", m.Planned, m.Address, m.Allowed)
}

// Changes extracts the real changes from a plan, sorted by address.
func Changes(plan *tfjson.Plan) ([]Change, error) {
	var out []Change
	for _, rc := range plan.ResourceChanges {
		if rc.Change == nil {
			continue
		}
		a, ok, err := classify(rc.Change.Actions)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rc.Address, err)
		}
		if ok {
			out = append(out, Change{Address: rc.Address, Action: a})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out, nil
}

func classify(a tfjson.Actions) (Action, bool, error) {
	switch {
	case a.NoOp(), a.Read():
		return "", false, nil
	case a.Create():
		return Create, true, nil
	case a.Update():
		return Update, true, nil
	case a.Delete():
		return Delete, true, nil
	case a.Replace():
		return Replace, true, nil
	}
	// An action set this code does not know is never waved through.
	return "", false, fmt.Errorf("unknown plan actions %v", a)
}

// Check returns every disagreement between the Intent and the changes.
func (i Intent) Check(changes []Change) []Mismatch {
	var out []Mismatch
	met := make([]bool, len(i.Expectations))
	for _, c := range changes {
		matched := false
		var allowed []Action
		for n, e := range i.Expectations {
			if !match(e.Address, c.Address) {
				continue
			}
			allowed = append(allowed, e.Actions...)
			if contains(e.Actions, c.Action) {
				matched = true
				met[n] = true
			}
		}
		if !matched {
			out = append(out, Mismatch{Address: c.Address, Planned: c.Action, Allowed: allowed})
		}
	}
	for n, e := range i.Expectations {
		if e.Required && !met[n] {
			out = append(out, Mismatch{Address: e.Address, Allowed: e.Actions})
		}
	}
	return out
}

func match(pattern, address string) bool {
	re := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$"
	return regexp.MustCompile(re).MatchString(address)
}

// Summary prints changes as `create hcloud_server.x` lines. It never prints
// attribute values: a plan holds the Hetzner token and seeded host keys.
func Summary(changes []Change) string {
	if len(changes) == 0 {
		return "  no changes\n"
	}
	var b strings.Builder
	for _, c := range changes {
		fmt.Fprintf(&b, "  %-8s %s\n", c.Action, c.Address)
	}
	return b.String()
}

func contains(as []Action, a Action) bool {
	for _, x := range as {
		if x == a {
			return true
		}
	}
	return false
}
