package secrets

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
)

// Cluster reads and applies Secrets on the live cluster.
type Cluster interface {
	// Secret returns the live Secret as YAML, or nil when it does not exist.
	Secret(ctx context.Context, ref catalog.Ref) ([]byte, error)
	// Apply applies one Secret manifest. With force it takes over keys that
	// another field manager owns; without it such a key stops the apply.
	Apply(ctx context.Context, manifest []byte, force bool) error
}

// Manifest returns the plaintext manifest of s, ready to apply. It refuses
// a file that does not exist, lacks a key, holds a key the catalog does not
// list, or has the wrong type or labels: a write command puts those right
// first, so the cluster only ever gets what the catalog says.
func Manifest(ctx context.Context, store *Store, s *catalog.Secret) ([]byte, Values, error) {
	v, found, mismatch, err := store.Read(ctx, s)
	switch {
	case err != nil:
		return nil, nil, err
	case !found:
		return nil, nil, fmt.Errorf("%s: %s does not exist: run `kluster secrets generate` or `pull`", s.Ref, s.File)
	case mismatch:
		return nil, nil, fmt.Errorf("%s: type or labels differ from %s: run `kluster secrets generate`", s.Ref, catalog.FileName)
	}
	for _, name := range s.KeyNames() {
		if _, ok := v[name]; !ok {
			return nil, nil, fmt.Errorf("%s.%s: %s", s.Ref, name, MissingHint(s.Keys[name]))
		}
	}
	if ex := extra(v, s); len(ex) > 0 {
		return nil, nil, fmt.Errorf("%s: %v not in %s: run `kluster secrets generate` to remove them", s.Ref, ex, catalog.FileName)
	}
	m, err := render(s, v)
	return m, v, err
}

// LiveDiff is how one live Secret differs from its file.
type LiveDiff struct {
	Secret *catalog.Secret
	// Absent is true when the Secret does not exist on the cluster.
	Absent  bool
	Changes []KeyChange
}

// Drift reports whether apply would change the Secret.
func (d LiveDiff) Drift() bool { return d.Absent || len(d.Changes) > 0 }

// Diff compares each Secret's file with the live Secret. It reads values
// to compare them and prints none.
func Diff(ctx context.Context, store *Store, cl Cluster, secrets []*catalog.Secret) ([]LiveDiff, error) {
	var out []LiveDiff
	for _, s := range secrets {
		_, want, err := Manifest(ctx, store, s)
		if err != nil {
			return nil, err
		}
		raw, err := cl.Secret(ctx, s.Ref)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.Ref, err)
		}
		d := LiveDiff{Secret: s}
		if raw == nil {
			d.Absent = true
			out = append(out, d)
			continue
		}
		sh, live, err := parse(raw)
		if err != nil {
			return nil, fmt.Errorf("live %s: %w", s.Ref, err)
		}
		if sh.typ != s.Type {
			d.Changes = append(d.Changes, KeyChange{"(type)", Change, fmt.Sprintf("live is %s: the type cannot change in place, delete the Secret first", sh.typ)})
		}
		for _, l := range sortedKeys(s.Labels) {
			if lv, ok := sh.labels[l]; !ok || lv != s.Labels[l] {
				d.Changes = append(d.Changes, KeyChange{"(label " + l + ")", Change, "follows " + catalog.FileName})
			}
		}
		for _, name := range s.KeyNames() {
			lv, ok := live[name]
			switch {
			case !ok:
				d.Changes = append(d.Changes, KeyChange{name, Add, "not on the cluster"})
			case !bytes.Equal(lv, want[name]):
				d.Changes = append(d.Changes, KeyChange{name, Change, "differs from the file"})
			}
		}
		for _, name := range extra(live, s) {
			d.Changes = append(d.Changes, KeyChange{name, Remove, "only on the cluster: apply removes it only if kluster applied it before"})
		}
		out = append(out, d)
	}
	return out, nil
}

// PrintDiff writes the diffs by key name and says whether anything drifts.
func PrintDiff(w io.Writer, diffs []LiveDiff) (drift bool, err error) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, d := range diffs {
		switch {
		case d.Absent:
			fmt.Fprintf(tw, "%s\tnot on the cluster: apply creates it\n", d.Secret.Ref)
		case len(d.Changes) > 0:
			fmt.Fprintf(tw, "%s\n", d.Secret.Ref)
			for _, c := range d.Changes {
				fmt.Fprintf(tw, "  %s %s\t%s\n", c.Op, c.Key, c.Reason)
			}
		default:
			fmt.Fprintf(tw, "%s\tin step\n", d.Secret.Ref)
		}
		drift = drift || d.Drift()
	}
	return drift, tw.Flush()
}

// Apply applies each Secret whose live copy drifts from its file. force is
// passed to the cluster: see Cluster.Apply.
func Apply(ctx context.Context, store *Store, cl Cluster, diffs []LiveDiff, force bool) error {
	for _, d := range diffs {
		if !d.Drift() {
			continue
		}
		m, _, err := Manifest(ctx, store, d.Secret)
		if err != nil {
			return err
		}
		if err := cl.Apply(ctx, m, force); err != nil {
			return fmt.Errorf("%s: %w", d.Secret.Ref, err)
		}
	}
	return nil
}

// Select returns the Secrets named "<namespace>/<name>", or every Secret
// when names is empty.
func Select(cat *catalog.Catalog, names []string) ([]*catalog.Secret, error) {
	if len(names) == 0 {
		return cat.Secrets, nil
	}
	out := make([]*catalog.Secret, 0, len(names))
	for _, n := range names {
		ref, err := catalog.ParseRef(n)
		if err != nil {
			return nil, err
		}
		s, ok := cat.Secret(ref)
		if !ok {
			return nil, fmt.Errorf("%s: no such Secret in %s", ref, catalog.FileName)
		}
		out = append(out, s)
	}
	return out, nil
}
