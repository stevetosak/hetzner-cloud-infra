package secrets

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"text/tabwriter"

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
			k := s.Keys[name]
			row := Row{s.Ref, name, string(k.Kind), Present}
			cur, ok := v[name]
			switch {
			case !ok:
				row.State = Missing
			case k.Kind == catalog.Copy:
				src := k.Source()
				if sv, ok := vals[src.Secret][src.Key]; ok && !bytes.Equal(k.Render(sv), cur) {
					row.State = StaleCopy
				}
			case k.Kind == catalog.Literal:
				if !bytes.Equal([]byte(k.Value), cur) {
					row.State = StaleCopy
				}
			case k.Kind == catalog.Issued && vault == nil:
				row.State = NotComparedBW
			case k.Kind == catalog.Issued:
				bw, err := vault.Issued(ctx, k)
				if err != nil {
					return nil, fmt.Errorf("%s.%s: %w", s.Ref, name, err)
				}
				if !bytes.Equal(bw, cur) {
					row.State = StaleIssued
				}
			}
			rows = append(rows, row)
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
