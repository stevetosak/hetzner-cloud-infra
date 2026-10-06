package stages

import "github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"

// At returns s carrying out another runbook section: a Control Plane Stage
// that a Worker runs too names the Workers runbook's section, not its own.
func At(s stage.Stage, runbook string) stage.Stage { return at{s, runbook} }

type at struct {
	stage.Stage
	runbook string
}

func (a at) Runbook() string { return a.runbook }
