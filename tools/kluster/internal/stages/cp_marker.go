package stages

import (
	"context"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/hostkey"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// The Control Plane build marker. cloud-init writes "running" at the first
// boot of a server `kluster cp init` created; the last Stage writes
// "complete". A k8s-cp without the marker was not built by kluster, and
// `cp init` refuses it: the slot is not empty (ADR 0009).
const (
	MarkerPath     = "/etc/kluster/cp-init"
	MarkerRunning  = "running"
	MarkerComplete = "complete"
)

// MarkerFile is the cloud-init file that starts a build.
func MarkerFile() hostkey.File {
	return hostkey.File{Path: MarkerPath, Content: MarkerRunning + "\n", Permissions: "0644"}
}

// ReadMarker returns the marker's value, or "" if the host has none.
func ReadMarker(ctx context.Context, h *stage.Host) (string, error) {
	out, err := h.Exec.Run(ctx, "cat "+MarkerPath+" 2>/dev/null || true")
	return strings.TrimSpace(out), err
}

// CompleteBuild is the last Stage: it marks the build complete, so a later
// `cp init` leaves this Control Plane alone.
type CompleteBuild struct{}

func (CompleteBuild) Name() string { return "complete-build" }
func (CompleteBuild) Runbook() string {
	return "docs/runbook/control-plane.md#how-kluster-carries-this-out"
}

func (CompleteBuild) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	m, err := ReadMarker(ctx, h)
	if err != nil {
		return stage.Status{}, err
	}
	if m == MarkerComplete {
		return stage.Status{Done: true, Detail: MarkerPath + " = " + m}, nil
	}
	return stage.Status{Detail: MarkerPath + " = " + m}, nil
}

func (CompleteBuild) Preview(context.Context, *stage.Host) (string, error) {
	return "echo " + MarkerComplete + " > " + MarkerPath + "\n", nil
}

func (CompleteBuild) Act(ctx context.Context, h *stage.Host) error {
	_, err := h.Exec.Run(ctx, "echo "+MarkerComplete+" > "+MarkerPath)
	return err
}
