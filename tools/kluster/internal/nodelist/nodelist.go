// Package nodelist puts the three records of a Worker side by side — the
// Worker set, the Hetzner servers and the Kubernetes Nodes — and marks where
// they disagree (`kluster node list`).
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
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

// Row is one Worker name and what each record holds for it. A nil field
// means that record has no such Worker.
type Row struct {
	Name   string
	Set    *workerset.Worker
	Server *cloud.Server
	Node   *kube.Node
	Drift  []string
}

// Input is what Compare reads. Nodes is nil when they could not be read,
// which is not the same as a cluster with no Workers.
type Input struct {
	Set          []workerset.Worker
	Servers      []*cloud.Server
	Nodes        []kube.Node
	NodesRead    bool
	ControlPlane string // left out of every record
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
	for _, n := range in.Nodes {
		if n.Name != in.ControlPlane && !n.ControlPlane {
			row(n.Name).Node = &n
		}
	}
	out := make([]Row, 0, len(rows))
	for _, name := range slices.Sorted(maps.Keys(rows)) {
		r := rows[name]
		r.Drift = drift(r, in.NodesRead)
		out = append(out, *r)
	}
	return out
}

func drift(r *Row, nodesRead bool) []string {
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
	fmt.Fprintln(tw, "WORKER\tSET (private, vpn)\tSERVER (status, private)\tNODE\tDRIFT")
	for _, r := range rows {
		set, srv, node := "-", "-", "-"
		if r.Set != nil {
			set = r.Set.PrivateIP + ", " + r.Set.VpnIP
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
		d := "-"
		if len(r.Drift) > 0 {
			d = "⚠ " + strings.Join(r.Drift, "; ")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Name, set, srv, node, d)
	}
	if len(rows) == 0 {
		fmt.Fprintln(tw, "(no Workers in any record)\t\t\t\t")
	}
	return tw.Flush()
}
