package secrets

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
)

// Print writes the plan by Secret and key name. It never writes a value.
// Files with nothing to change and nothing missing are left out.
func (p *Plan) Print(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	changed := 0
	for _, f := range p.Files {
		if !f.Changed() && len(f.Missing) == 0 {
			continue
		}
		if f.Changed() {
			changed++
		}
		head := f.Secret.Ref.String() + "\t" + f.Secret.File
		if !f.Found {
			head += " (new file)"
		}
		fmt.Fprintln(tw, head)
		if f.Reshape {
			fmt.Fprintln(tw, "  ~ "+shapeKeyMarker+"\tfollow "+catalog.FileName)
		}
		for _, c := range f.Changes {
			fmt.Fprintf(tw, "  %s %s\t%s\n", c.Op, c.Key, c.Reason)
		}
		for _, name := range f.Missing {
			fmt.Fprintf(tw, "  ! %s\t%s\n", name, MissingHint(f.Secret.Keys[name]))
		}
	}
	switch changed {
	case 0:
		fmt.Fprintln(tw, "No file changes.")
	case 1:
		fmt.Fprintln(tw, "1 file changes.")
	default:
		fmt.Fprintf(tw, "%d files change.\n", changed)
	}
	return tw.Flush()
}

// MissingHint says how a key without a value gets one.
func MissingHint(k *catalog.Key) string {
	switch k.Kind {
	case catalog.Generated:
		return "no value: run `kluster secrets generate`"
	case catalog.Issued:
		return "no value: store it in Bitwarden (" + IssuedSource(k) + ") and run `kluster secrets pull`, or `kluster secrets set`"
	case catalog.Copy:
		return "no value: its source " + k.Source().String() + " has none"
	default:
		return "no value"
	}
}
