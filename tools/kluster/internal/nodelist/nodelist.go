// Package nodelist puts the records of a Worker side by side — the Worker set
// or the Node set's byo machines, the Hetzner servers and the Kubernetes
// Nodes — and marks where they disagree with each other or with the profile
// of their site (`kluster node list`).
package nodelist

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/nodeset"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

// Row is one Worker name and what each record holds for it. A nil field
// means that record has no such Worker.
type Row struct {
	Name    string
	Set     *workerset.Worker
	Machine *nodeset.Machine // a byo machine of the Node set
	Server  *cloud.Server
	Node    *kube.Node
	Drift   []string
}

// Input is what Compare reads. Nodes is nil when they could not be read,
// which is not the same as a cluster with no Workers.
type Input struct {
	Set          []workerset.Worker
	Servers      []*cloud.Server
	Nodes        []kube.Node
	NodesRead    bool
	ControlPlane string       // left out of every record
	Site         nodeset.Site // the site every Hetzner Worker belongs to
	// Byo are the Node set's byo machines: each belongs to its own site and
	// has no Hetzner server.
	Byo []nodeset.Machine
}

// Compare returns one Row per Worker name, sorted.
func Compare(in Input) []Row {
	rows := map[string]*Row{}
	row := func(name string) *Row {
		if rows[name] == nil {
			rows[name] = &Row{Name: name}
		}
		return rows[name]
	}
	for _, w := range in.Set {
		row(w.Name).Set = &w
	}
	for _, s := range in.Servers {
		if s.Name != in.ControlPlane {
			row(s.Name).Server = s
		}
	}
	for _, m := range in.Byo {
		row(m.Name).Machine = &m
	}
	for _, n := range in.Nodes {
		if n.Name != in.ControlPlane && !n.ControlPlane {
			row(n.Name).Node = &n
		}
	}
	out := make([]Row, 0, len(rows))
	for _, name := range slices.Sorted(maps.Keys(rows)) {
		r := rows[name]
		r.Drift = drift(r, in.Site, in.NodesRead)
		out = append(out, *r)
	}
	return out
}

func drift(r *Row, site nodeset.Site, nodesRead bool) []string {
	if r.Machine != nil {
		return byoDrift(r, nodesRead)
	}
	var d []string
	switch {
	case r.Set != nil && r.Server == nil:
		d = append(d, "no server")
	case r.Set == nil && r.Server != nil:
		d = append(d, "server not in the Worker set")
	case r.Set != nil && r.Server != nil:
		if !slices.Equal(r.Server.PrivateIPs, []string{r.Set.PrivateIP}) {
			d = append(d, fmt.Sprintf("server private IP %v, set %s", r.Server.PrivateIPs, r.Set.PrivateIP))
		}
		if !r.Server.Running() {
			d = append(d, "server "+r.Server.Status)
		}
	}
	if r.Server != nil && r.Server.Location != site.Name {
		d = append(d, fmt.Sprintf("server in %s, site %s", r.Server.Location, site.Name))
	}
	if !nodesRead {
		return d
	}
	switch {
	case r.Node == nil && r.Server != nil:
		d = append(d, "not joined")
	case r.Node != nil && r.Server == nil:
		d = append(d, "Node without a server")
	}
	if r.Node != nil {
		if !r.Node.Ready {
			d = append(d, "NotReady")
		}
		if r.Set != nil && r.Node.InternalIP != r.Set.PrivateIP {
			d = append(d, fmt.Sprintf("Node InternalIP %s, set %s", r.Node.InternalIP, r.Set.PrivateIP))
		}
		if r.Set != nil {
			d = append(d, profileDrift(r.Node, site)...)
		}
	}
	return d
}

// byoDrift is the drift of a byo machine: it has no Worker set entry and no
// Hetzner server, and its Node stands on its mesh address with the providerID
// its kubelet set.
func byoDrift(r *Row, nodesRead bool) []string {
	var d []string
	m := r.Machine
	if r.Set != nil {
		d = append(d, "also in the Worker set")
	}
	if r.Server != nil {
		d = append(d, "a Hetzner server has its name")
	}
	if !nodesRead {
		return d
	}
	if r.Node == nil {
		return append(d, "not joined")
	}
	if !r.Node.Ready {
		d = append(d, "NotReady")
	}
	if r.Node.InternalIP != m.Address.String() {
		d = append(d, fmt.Sprintf("Node InternalIP %s, mesh address %s", r.Node.InternalIP, m.Address))
	}
	if r.Node.ProviderID != m.ProviderID() {
		d = append(d, fmt.Sprintf("providerID %q, want %s", r.Node.ProviderID, m.ProviderID()))
	}
	return append(d, profileDrift(r.Node, m.Site)...)
}

// profileDrift names what a Node lacks of its site's profile: the labels and
// the reliability taint.
func profileDrift(n *kube.Node, site nodeset.Site) []string {
	var d []string
	if c := nodeset.Diff(n.Labels, site.Labels()); !c.Empty() {
		d = append(d, "profile: "+c.String())
	}
	if c := nodeset.DiffTaint(n.Taints, site); !c.Empty() {
		d = append(d, "profile: "+c.String())
	}
	return d
}

// HasDrift reports whether any Row disagrees.
func HasDrift(rows []Row) bool {
	return slices.ContainsFunc(rows, func(r Row) bool { return len(r.Drift) > 0 })
}

// Print writes rows as a table. nodesRead false prints the Node column as
// unknown.
func Print(w io.Writer, rows []Row, nodesRead bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKER\tSET (private, vpn)\tSERVER (status, private)\tNODE\tPROFILE\tDRIFT")
	for _, r := range rows {
		set, srv, node, profile := "-", "-", "-", "-"
		switch {
		case r.Set != nil:
			set = r.Set.PrivateIP + ", " + r.Set.VpnIP
		case r.Machine != nil:
			set = "byo " + r.Machine.Site.Name + ", mesh " + r.Machine.Address.String()
		}
		if r.Server != nil {
			srv = r.Server.Status + ", " + strings.Join(r.Server.PrivateIPs, " ")
		}
		switch {
		case !nodesRead:
			node = "?"
		case r.Node != nil && r.Node.Ready:
			node = "Ready " + r.Node.Kubelet
		case r.Node != nil:
			node = "NotReady " + r.Node.Kubelet
		}
		switch {
		case !nodesRead:
			profile = "?"
		case r.Node != nil && r.Node.Labels[nodeset.Prefix+nodeset.KeySite] != "":
			profile = r.Node.Labels[nodeset.Prefix+nodeset.KeySite]
		}
		d := "-"
		if len(r.Drift) > 0 {
			d = "⚠ " + strings.Join(r.Drift, "; ")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, set, srv, node, profile, d)
	}
	if len(rows) == 0 {
		fmt.Fprintln(tw, "(no Workers in any record)\t\t\t\t\t")
	}
	return tw.Flush()
}
