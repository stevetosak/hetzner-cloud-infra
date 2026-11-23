package cmd

import (
	"context"
	"fmt"
	"slices"

	"github.com/spf13/cobra"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/kube"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/nodeset"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stages"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

func init() {
	nodeCmd.AddCommand(nodeProfileCmd)
}

var nodeProfileCmd = &cobra.Command{
	Use:   "profile [<name>]",
	Short: "Write and check each Node's site profile as admin labels (docs/runbook/workers.md)",
	Long: "Every Node, the Control Plane included, or the named one. The Control Plane and each Worker of the " +
		"Worker set belong to the Node set's hcloud site; kluster refuses a Node whose Hetzner server stands " +
		"in another location. On the Control Plane it sets the site's labels under " +
		nodeset.Prefix + ", removes any other key under that prefix, and reads them back. Labels outside the " +
		"prefix stay. A byo machine of the Node set gets its own site's profile. A Node kluster knows no site for is reported, not changed. " +
		"node add and cp init run the same Stage after each join. Live goes through WireGuard and opens " +
		"nothing; a rehearsal opens bootstrap SSH on the Control Plane firewall and closes it at the end.",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		only := ""
		if len(args) == 1 {
			only = args[0]
		}
		return run(cmd, func(ctx context.Context, a *app) error { return nodeProfile(ctx, a, only) })
	},
}

func nodeProfile(ctx context.Context, a *app, only string) error {
	site, err := a.hcloudSite()
	if err != nil {
		return err
	}
	path, err := a.workerSetPath()
	if err != nil {
		return err
	}
	set, err := workerset.Load(path)
	if err != nil {
		return err
	}
	servers, err := a.api.Servers(ctx)
	if err != nil {
		return err
	}
	if !a.env.IsLive() {
		a.warnOpenSSH(ctx)
	}
	nodeSet, err := a.nodeSet()
	if err != nil {
		return err
	}
	return a.onControlPlane(ctx, func(cp *stage.Host) error {
		if cp == nil {
			fmt.Fprintln(a.out, "Plan Mode: the Control Plane was not reached, so no Node is read; --apply opens bootstrap SSH and reads them")
			return nil
		}
		nodes, err := kube.Kubectl{Exec: cp.Exec}.Nodes(ctx)
		if err != nil {
			return err
		}
		h := nodeSites{controlPlane: a.cfg.ControlPlane.Name, workers: set.Names(), servers: servers, site: site, byo: byoMachines(nodeSet)}
		targets, problems := h.targets(nodes, only)
		opts := stage.Options{Apply: a.mode.Apply, Out: a.out}
		for _, t := range targets {
			if err := stage.Run(ctx, []stage.Stage{t}, cp, opts); err != nil {
				return err
			}
		}
		for _, p := range problems {
			fmt.Fprintln(a.out, "refused:", p)
		}
		if len(problems) > 0 {
			return fmt.Errorf("node profile: %d Node(s) refused; the others are above", len(problems))
		}
		if !a.mode.Apply {
			fmt.Fprintln(a.out, "Plan Mode: --apply runs the Stages above")
		}
		return nil
	})
}

// nodeSites is what tells kluster a Node's site: the Control Plane and the
// Worker set are Hetzner servers, and every Hetzner server belongs to the
// hcloud site; a byo machine of the Node set belongs to its own site.
type nodeSites struct {
	controlPlane string
	workers      []string
	servers      []*cloud.Server
	site         nodeset.Site
	byo          []nodeset.Machine
}

// targets are the profile Stages of the Nodes, or of the one named only,
// and the Nodes refused: a Node kluster knows no site for, a Node with no
// server behind it, a server outside the site's location, and a named Node
// the cluster does not have.
func (h nodeSites) targets(nodes []kube.Node, only string) ([]stages.NodeProfile, []error) {
	var targets []stages.NodeProfile
	var problems []error
	found := false
	for _, n := range nodes {
		if only != "" && n.Name != only {
			continue
		}
		found = true
		if n.Name != h.controlPlane && !slices.Contains(h.workers, n.Name) {
			if i := slices.IndexFunc(h.byo, func(m nodeset.Machine) bool { return m.Name == n.Name }); i >= 0 {
				targets = append(targets, stages.NodeProfile{Node: n.Name, Site: h.byo[i].Site})
				continue
			}
			problems = append(problems, fmt.Errorf("Node %s is neither %s, in the Worker set nor a byo machine of the Node set: kluster knows no site for it", n.Name, h.controlPlane))
			continue
		}
		i := slices.IndexFunc(h.servers, func(s *cloud.Server) bool { return s.Name == n.Name })
		if i < 0 {
			problems = append(problems, fmt.Errorf("Node %s has no Hetzner server: kluster node replace builds it again", n.Name))
			continue
		}
		if err := checkSite(h.servers[i], h.site); err != nil {
			problems = append(problems, err)
			continue
		}
		targets = append(targets, stages.NodeProfile{Node: n.Name, Site: h.site})
	}
	if only != "" && !found {
		problems = append(problems, fmt.Errorf("the cluster has no Node %s", only))
	}
	return targets, problems
}
