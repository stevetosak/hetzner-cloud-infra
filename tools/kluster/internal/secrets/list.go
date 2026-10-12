package secrets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/bitwarden"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
)

// State is what list says about one key. It never holds a value.
type State string

const (
	Present        State = "present"
	Missing        State = "missing"
	StaleCopy      State = "differs from its source: run `kluster secrets generate`"
	StaleIssued    State = "differs from Bitwarden: run `kluster secrets pull`"
	NotInCatalog   State = "not in " + catalog.FileName + ": the next write removes it"
	WrongShape     State = "type or labels differ from " + catalog.FileName + ": the next write fixes them"
	NotComparedBW  State = "present, not compared with Bitwarden"
	NotInBitwarden State = "present, its Bitwarden item does not exist: store the value there"
	SourceMissing  State = "present, not compared: its source is missing"
	MissingInBW    State = "missing; Bitwarden has it: run `kluster secrets pull`"
	MissingNoBW    State = "missing, and its Bitwarden item does not exist: store the value there, then pull"
	shapeKeyMarker       = "(type, labels)"
)

// Row is one line of list.
type Row struct {
	Secret catalog.Ref
	Key    string
	Kind   string
	State  State
}

// List reports every key of every Secret: its kind and its state. Copies
// are compared with their source; issued keys with Bitwarden when vault is
// not nil.
func List(ctx context.Context, store *Store, vault Vault) ([]Row, error) {
	cat := store.cat
	vals := map[catalog.Ref]Values{}
	var rows []Row
	for _, s := range cat.Secrets {
		v, _, mismatch, err := store.Read(ctx, s)
		if err != nil {
			return nil, err
		}
		vals[s.Ref] = v
		if mismatch {
			rows = append(rows, Row{s.Ref, shapeKeyMarker, "", WrongShape})
		}
	}
	for _, s := range cat.Secrets {
		v := vals[s.Ref]
		for _, name := range s.KeyNames() {
			cur, ok := v[name]
			st, err := keyState(ctx, s.Keys[name], cur, ok, vals, vault)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", s.Ref, name, err)
			}
			rows = append(rows, Row{s.Ref, name, string(s.Keys[name].Kind), st})
		}
		for _, name := range extra(v, s) {
			rows = append(rows, Row{s.Ref, name, "", NotInCatalog})
		}
	}
	return rows, nil
}

// PrintList writes the rows as a table.
func PrintList(w io.Writer, rows []Row) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SECRET\tKEY\tKIND\tSTATE")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Secret, r.Key, r.Kind, r.State)
	}
	return tw.Flush()
}

// keyState compares one key (its value cur, ok when the file holds it) with
// where its value comes from: its source Secret (vals) or Bitwarden. A nil
// vault skips Bitwarden.
func keyState(ctx context.Context, k *catalog.Key, cur []byte, ok bool, vals map[catalog.Ref]Values, vault Vault) (State, error) {
	switch k.Kind {
	case catalog.Copy:
		if !ok {
			return Missing, nil
		}
		src := k.Source()
		sv, found := vals[src.Secret][src.Key]
		switch {
		case !found:
			return SourceMissing, nil
		case !bytes.Equal(k.Render(sv), cur):
			return StaleCopy, nil
		}
	case catalog.Literal:
		if !ok {
			return Missing, nil
		}
		if !bytes.Equal([]byte(k.Value), cur) {
			return StaleCopy, nil
		}
	case catalog.Issued:
		if vault == nil {
			if !ok {
				return Missing, nil
			}
			return NotComparedBW, nil
		}
		bw, err := vault.Issued(ctx, k)
		inBW := !errors.Is(err, bitwarden.ErrNotFound)
		if err != nil && inBW {
			return "", err
		}
		switch {
		case !ok && inBW:
			return MissingInBW, nil
		case !ok:
			return MissingNoBW, nil
		case !inBW:
			return NotInBitwarden, nil
		case !bytes.Equal(bw, cur):
			return StaleIssued, nil
		}
	default:
		if !ok {
			return Missing, nil
		}
	}
	return Present, nil
}
