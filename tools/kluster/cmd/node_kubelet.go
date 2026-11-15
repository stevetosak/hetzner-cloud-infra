package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stages"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/workerset"
)

func init() {
	nodeCmd.AddCommand(nodeKubeletConfigCmd)
}

var nodeKubeletConfigCmd = &cobra.Command{
	Use:   "kubelet-config <name>",
	Short: "Set kluster.yaml's kubelet reserve and eviction values on one Worker (docs/runbook/workers.md)",
	Long: "One Worker per run. On the Control Plane kluster refuses a Worker whose pods request more memory than " +
		"it offers with the new values, then sets the values in the kube-system kubelet-config ConfigMap (a new " +
		"Worker gets them at its join). On the Worker it backs up /var/lib/kubelet/config.yaml, runs `kubeadm " +
		"upgrade node phase kubelet-config` and restarts the kubelet. Back on the Control Plane it proves the " +
		"running configuration (configz), the Node's allocatable memory and that no pod was rejected. Live goes " +
		"through WireGuard and opens nothing; the Worker needs a pin at its VPN address (kluster pin import). A " +
		"rehearsal opens bootstrap SSH on both firewalls and closes it at the end.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return run(cmd, func(ctx context.Context, a *app) error { return nodeKubeletConfig(ctx, a, args[0]) })
	},
}

// kubeletWait is how long the restart and the proof wait for the kubelet.
const kubeletWait = 2 * time.Minute

// kubeletSteps are the kubelet Stages of one Worker, by the host they run
// on, in order: before on the Control Plane, node on the Worker, proof on
// the Control Plane. cp is the Control Plane's Exec, through which the
// restart reads the running configuration.
type kubeletSteps struct {
	before, node, proof []stage.Stage
}

func kubeletStages(name string, want map[string]any, cp stage.Exec) kubeletSteps {
	return kubeletSteps{
		before: []stage.Stage{stages.KubeletRoom{Node: name, Want: want}, stages.KubeletConfigMap{Want: want}},
		node: []stage.Stage{
			stages.KubeletConfigFile{Want: want, Now: time.Now},
			stages.KubeletRestart{Node: name, Want: want, CP: cp, Wait: kubeletWait, Every: 3 * time.Second},
		},
		proof: []stage.Stage{stages.KubeletProof{Node: name, Want: want, Wait: kubeletWait, Every: 5 * time.Second}},
	}
}

func nodeKubeletConfig(ctx context.Context, a *app, name string) error {
	if err := checkWorkerName(name, a.cfg.ControlPlane.Name); err != nil {
		return err
	}
	want := a.cfg.Kubelet.Fields()
	if len(want) == 0 {
		return errors.New("kluster.yaml sets no kubelet values: nothing to set")
	}
	path, err := a.workerSetPath()
	if err != nil {
		return err
	}
	set, err := workerset.Load(path)
	if err != nil {
		return err
	}
	w, ok := set.Get(name)
	if !ok {
		return fmt.Errorf("%s declares no worker %s", path, name)
	}
	if a.env.IsLive() {
		return a.kubeletConfig(ctx, w, want)
	}
	a.warnOpenSSH(ctx)
	return a.withBootstrapSSH(ctx, func() error {
		if !a.mode.Apply {
			fmt.Fprintln(a.out, "Plan Mode: bootstrap SSH stays closed, so neither host is read")
			s := kubeletStages(name, want, nil)
			for _, run := range []struct {
				host  string
				steps []stage.Stage
			}{{a.cfg.ControlPlane.Name, s.before}, {name, s.node}, {a.cfg.ControlPlane.Name, s.proof}} {
				if err := stage.PlanNew(ctx, run.steps, run.host, a.out); err != nil {
					return err
				}
			}
			return nil
		}
		return a.kubeletConfig(ctx, w, want)
	})
}

// kubeletConfig reaches both hosts first, so a missing pin stops the run
// before anything changes, then runs the Stages in order.
func (a *app) kubeletConfig(ctx context.Context, w workerset.Worker, want map[string]any) error {
	cp, closeCP, err := a.dialControlPlane(ctx)
	if err != nil {
		return err
	}
	defer closeCP()
	wh, closeW, err := a.dialWorker(ctx, w)
	if err != nil {
		return err
	}
	defer closeW()
	s := kubeletStages(w.Name, want, cp.Exec)
	opts := stage.Options{Apply: a.mode.Apply, Out: a.out}
	for _, run := range []struct {
		h     *stage.Host
		steps []stage.Stage
	}{{cp, s.before}, {wh, s.node}, {cp, s.proof}} {
		if err := stage.Run(ctx, run.steps, run.h, opts); err != nil {
			return err
		}
	}
	if !a.mode.Apply {
		fmt.Fprintln(a.out, "Plan Mode: --apply runs the Stages above")
	}
	return nil
}
