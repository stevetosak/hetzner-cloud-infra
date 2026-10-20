package secrets

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
)

// Op is what a plan does to one key.
type Op string

const (
	Add    Op = "+"
	Change Op = "~"
	Remove Op = "-"
)

// KeyChange is one planned change. It names the key and why, never a value.
type KeyChange struct {
	Key    string
	Op     Op
	Reason string
}

// FilePlan is what a plan does to one encrypted file.
type FilePlan struct {
	Secret *catalog.Secret
	// Found is false when the file does not exist yet.
	Found bool
	// Reshape is true when the file's type or labels differ from the
	// catalog: a write puts them right even if no value changes.
	Reshape bool
	Changes []KeyChange
	// Missing are catalog keys that still have no value after the plan.
	Missing []string

	next Values
}

// Changed reports whether the plan writes the file.
func (f *FilePlan) Changed() bool { return len(f.Changes) > 0 || f.Reshape }

// Plan is the result of one write command, shown in Plan Mode and written
// with --apply.
type Plan struct {
	Files []*FilePlan
}

// Changed reports whether the plan writes any file.
func (p *Plan) Changed() bool {
	for _, f := range p.Files {
		if f.Changed() {
			return true
		}
	}
	return false
}

// update is a new value for a Generated or Issued key, with the reason.
type update struct {
	value  []byte
	reason string
}

// Planner computes plans over a Store.
type Planner struct {
	store  *Store
	vault  Vault
	random func(n int) ([]byte, error)
}

// NewPlanner returns a Planner. vault may be nil for commands that never
// read an issued value (generate, rotate).
func NewPlanner(store *Store, vault Vault) *Planner {
	return &Planner{store: store, vault: vault, random: Random}
}

// ErrIssuedRotate is returned when rotate names an issued key: only the
// provider issues a new value.
var ErrIssuedRotate = errors.New("an issued value is rotated at its provider: issue the new value there, store it in Bitwarden, then run pull")

// Generate plans a value for every generated key that has none, and
// recomputes every copy and literal.
func (p *Planner) Generate(ctx context.Context) (*Plan, error) {
	cur, err := p.current(ctx)
	if err != nil {
		return nil, err
	}
	ups := map[catalog.KeyRef]update{}
	for _, s := range p.store.cat.Secrets {
		for _, name := range s.KeyNames() {
			k := s.Keys[name]
			if k.Kind != catalog.Generated {
				continue
			}
			if _, ok := cur.values(s.Ref)[name]; ok {
				continue
			}
			v, err := p.random(k.Length)
			if err != nil {
				return nil, err
			}
			ups[catalog.KeyRef{Secret: s.Ref, Key: name}] = update{v, fmt.Sprintf("generated, %d characters", k.Length)}
		}
	}
	return p.build(cur, ups), nil
}

// Rotate plans a new value for each named generated key, or for every
// generated key when refs is empty, and rewrites every copy in the same
// plan.
func (p *Planner) Rotate(ctx context.Context, refs []catalog.KeyRef) (*Plan, error) {
	targets, err := p.resolve(refs, catalog.Generated, ErrIssuedRotate)
	if err != nil {
		return nil, err
	}
	cur, err := p.current(ctx)
	if err != nil {
		return nil, err
	}
	ups := map[catalog.KeyRef]update{}
	for _, t := range targets {
		v, err := p.random(t.key.Length)
		if err != nil {
			return nil, err
		}
		ups[t.ref] = update{v, fmt.Sprintf("rotated, %d characters", t.key.Length)}
	}
	return p.build(cur, ups), nil
}

// Pull plans the Bitwarden value of each named issued key, or of every
// issued key when refs is empty.
func (p *Planner) Pull(ctx context.Context, refs []catalog.KeyRef) (*Plan, error) {
	if p.vault == nil {
		return nil, errors.New("pull needs Bitwarden")
	}
	targets, err := p.resolve(refs, catalog.Issued, nil)
	if err != nil {
		return nil, err
	}
	cur, err := p.current(ctx)
	if err != nil {
		return nil, err
	}
	ups := map[catalog.KeyRef]update{}
	for _, t := range targets {
		v, err := p.vault.Issued(ctx, t.key)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t.ref, err)
		}
		if len(v) == 0 {
			return nil, fmt.Errorf("%s: %s is empty", t.ref, IssuedSource(t.key))
		}
		ups[t.ref] = update{v, "from Bitwarden, " + IssuedSource(t.key)}
	}
	return p.build(cur, ups), nil
}

// Set plans one issued key's value, typed by the operator.
func (p *Planner) Set(ctx context.Context, ref catalog.KeyRef, value []byte) (*Plan, error) {
	targets, err := p.resolve([]catalog.KeyRef{ref}, catalog.Issued, nil)
	if err != nil {
		return nil, err
	}
	if len(value) == 0 {
		return nil, fmt.Errorf("%s: the value is empty", ref)
	}
	cur, err := p.current(ctx)
	if err != nil {
		return nil, err
	}
	return p.build(cur, map[catalog.KeyRef]update{targets[0].ref: {value, "typed in the terminal"}}), nil
}

// Write writes every changed file of the plan.
func (p *Planner) Write(ctx context.Context, plan *Plan) error {
	for _, f := range plan.Files {
		if !f.Changed() {
			continue
		}
		if err := p.store.Write(ctx, f.Secret, f.next); err != nil {
			return err
		}
	}
	return nil
}

type target struct {
	ref catalog.KeyRef
	key *catalog.Key
}

// resolve resolves refs to keys of kind want; empty refs means every key of
// that kind. A named key of another kind is refused, with wrongKind if set.
func (p *Planner) resolve(refs []catalog.KeyRef, want catalog.Kind, wrongKind error) ([]target, error) {
	cat := p.store.cat
	if len(refs) == 0 {
		var all []target
		for _, s := range cat.Secrets {
			for _, name := range s.KeyNames() {
				if k := s.Keys[name]; k.Kind == want {
					all = append(all, target{catalog.KeyRef{Secret: s.Ref, Key: name}, k})
				}
			}
		}
		return all, nil
	}
	out := make([]target, 0, len(refs))
	for _, r := range refs {
		s, ok := cat.Secret(r.Secret)
		if !ok {
			return nil, fmt.Errorf("%s: no such Secret in %s", r.Secret, catalog.FileName)
		}
		k, ok := s.Keys[r.Key]
		if !ok {
			return nil, fmt.Errorf("%s: no such key in %s", r, catalog.FileName)
		}
		if k.Kind != want {
			if k.Kind == catalog.Issued && wrongKind != nil {
				return nil, fmt.Errorf("%s: %w", r, wrongKind)
			}
			return nil, fmt.Errorf("%s is a %s key, not %s", r, k.Kind, want)
		}
		out = append(out, target{r, k})
	}
	return out, nil
}

// state is every file's current content.
type state map[catalog.Ref]*fileState

type fileState struct {
	values   Values
	found    bool
	mismatch bool
}

func (st state) values(r catalog.Ref) Values {
	if f, ok := st[r]; ok {
		return f.values
	}
	return nil
}

func (p *Planner) current(ctx context.Context) (state, error) {
	st := state{}
	for _, s := range p.store.cat.Secrets {
		v, found, mismatch, err := p.store.Read(ctx, s)
		if err != nil {
			return nil, err
		}
		st[s.Ref] = &fileState{values: v, found: found, mismatch: mismatch}
	}
	return st, nil
}

// build applies ups to the current state, then derives every copy and
// literal from the catalog and drops keys the catalog does not list. It
// compares the result with the current state, key by key.
func (p *Planner) build(cur state, ups map[catalog.KeyRef]update) *Plan {
	cat := p.store.cat
	next := map[catalog.Ref]Values{}
	reasons := map[catalog.KeyRef]string{}
	// Sources first: copies read them.
	for _, s := range cat.Secrets {
		v := Values{}
		for _, name := range s.KeyNames() {
			ref := catalog.KeyRef{Secret: s.Ref, Key: name}
			if u, ok := ups[ref]; ok {
				v[name] = u.value
				reasons[ref] = u.reason
				continue
			}
			if k := s.Keys[name]; k.Kind == catalog.Generated || k.Kind == catalog.Issued {
				if old, ok := cur.values(s.Ref)[name]; ok {
					v[name] = old
				}
			}
		}
		next[s.Ref] = v
	}
	for _, s := range cat.Secrets {
		for _, name := range s.KeyNames() {
			ref := catalog.KeyRef{Secret: s.Ref, Key: name}
			switch k := s.Keys[name]; k.Kind {
			case catalog.Literal:
				next[s.Ref][name] = []byte(k.Value)
				reasons[ref] = "literal in " + catalog.FileName
			case catalog.Copy:
				src := k.Source()
				if sv, ok := next[src.Secret][src.Key]; ok {
					next[s.Ref][name] = k.Render(sv)
					reasons[ref] = "copy of " + src.String()
				} else if old, ok := cur.values(s.Ref)[name]; ok {
					next[s.Ref][name] = old
				}
			}
		}
	}

	plan := &Plan{}
	for _, s := range cat.Secrets {
		f := cur[s.Ref]
		fp := &FilePlan{Secret: s, Found: f.found, Reshape: f.mismatch, next: next[s.Ref]}
		old, nv := f.values, next[s.Ref]
		names := append(s.KeyNames(), extra(old, s)...)
		for _, name := range names {
			ref := catalog.KeyRef{Secret: s.Ref, Key: name}
			o, had := old[name]
			n, has := nv[name]
			switch {
			case !has && had:
				fp.Changes = append(fp.Changes, KeyChange{name, Remove, "not in " + catalog.FileName})
			case !has:
				fp.Missing = append(fp.Missing, name)
			case !had:
				fp.Changes = append(fp.Changes, KeyChange{name, Add, reasons[ref]})
			case !bytes.Equal(o, n):
				fp.Changes = append(fp.Changes, KeyChange{name, Change, reasons[ref]})
			}
		}
		plan.Files = append(plan.Files, fp)
	}
	return plan
}

// extra returns the keys of v that s does not list, in order.
func extra(v Values, s *catalog.Secret) []string {
	var out []string
	for _, name := range v.Keys() {
		if _, ok := s.Keys[name]; !ok {
			out = append(out, name)
		}
	}
	return out
}
