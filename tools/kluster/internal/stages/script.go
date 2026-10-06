package stages

import (
	"context"
	"fmt"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// scriptDir keeps every Act script kluster ran on a host, so the host itself
// records what built it. Scripts hold no secret.
const scriptDir = "/var/lib/kluster/scripts"

// adminKubectl is how the Control Plane Stages reach the API server: on the
// host, with the admin.conf kubeadm wrote.
const adminKubectl = "export KUBECONFIG=/etc/kubernetes/admin.conf\n"

// strict is the header of every Act script: a failure stops the host half
// built rather than going on past it (docs/runbook/control-plane.md, step 2).
const strict = "set -euo pipefail\nexport DEBIAN_FRONTEND=noninteractive\n"

// runScript writes body to the host as <scriptDir>/<name>.sh and runs it.
func runScript(ctx context.Context, h *stage.Host, name, body string) (string, error) {
	if _, err := h.Exec.Run(ctx, "mkdir -p -m 700 "+scriptDir); err != nil {
		return "", err
	}
	path := scriptDir + "/" + name + ".sh"
	if err := h.Exec.WriteFile(ctx, path, strict+body, 0o700); err != nil {
		return "", err
	}
	return h.Exec.Run(ctx, "bash "+path)
}

// check is one line of a Probe: cmd exits 0 when it holds.
type check struct{ name, cmd string }

// probe runs every check in one read-only command and reports the ones that
// fail. The Stage is done when none fails.
func probe(ctx context.Context, h *stage.Host, checks []check) (stage.Status, error) {
	var b strings.Builder
	for _, c := range checks {
		fmt.Fprintf(&b, "if ! ( %s ) >/dev/null 2>&1; then echo %q; fi\n", c.cmd, c.name)
	}
	b.WriteString("true\n")
	out, err := h.Exec.Run(ctx, b.String())
	if err != nil {
		return stage.Status{}, err
	}
	if failed := strings.TrimSpace(out); failed != "" {
		return stage.Status{Detail: "not yet: " + strings.ReplaceAll(failed, "\n", ", ")}, nil
	}
	names := make([]string, len(checks))
	for i, c := range checks {
		names[i] = c.name
	}
	return stage.Status{Done: true, Detail: strings.Join(names, ", ")}, nil
}

// heredoc renders a file write in an Act script. content must not hold the
// line EOF.
func heredoc(path, content string) string {
	return "cat > " + path + " <<'EOF'\n" + strings.TrimRight(content, "\n") + "\nEOF\n"
}
