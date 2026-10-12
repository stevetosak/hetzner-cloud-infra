package kube

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
)

// FieldManager is the server-side apply manager kluster secrets applies as.
const FieldManager = "kluster"

// Runner runs kubectl on the laptop with stdin and returns stdout and
// stderr. Tests replace it.
type Runner func(ctx context.Context, stdin []byte, args ...string) (stdout, stderr []byte, err error)

// Local runs kubectl on the laptop, against one named context of one
// kubeconfig, never the current context. It serves kluster secrets, which
// acts on the live cluster only: a rehearsal kubeconfig points at an address
// the laptop routes to the live hub (see the package comment).
type Local struct {
	Kubeconfig string
	Context    string
	run        Runner
}

// NewLocal returns a Local over the kubectl on PATH.
func NewLocal(kubeconfig, context string) *Local {
	return &Local{Kubeconfig: kubeconfig, Context: context, run: localKubectl}
}

// NewLocalWithRunner returns a Local over run.
func NewLocalWithRunner(kubeconfig, context string, run Runner) *Local {
	return &Local{Kubeconfig: kubeconfig, Context: context, run: run}
}

// ContextName is the kubeconfig context kluster writes for a cluster.
func ContextName(clusterName string) string { return clusterName + "-admin@" + clusterName }

func localKubectl(ctx context.Context, stdin []byte, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	// kubectl needs HOME for its cache; nothing else from the shell.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.Bytes(), errb.Bytes(), err
}

func (l *Local) args(rest ...string) []string {
	return append([]string{"--kubeconfig", l.Kubeconfig, "--context", l.Context}, rest...)
}

// Secret returns the live Secret as YAML, or nil when it does not exist.
// The output holds values: it is for comparing, never for printing.
func (l *Local) Secret(ctx context.Context, ref catalog.Ref) ([]byte, error) {
	out, stderr, err := l.run(ctx, nil, l.args("get", "secret", "-n", ref.Namespace, ref.Name, "-o", "yaml", "--ignore-not-found")...)
	if err != nil {
		return nil, fmt.Errorf("kubectl get secret: %w: %s", err, firstLine(stderr))
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
	}
	return out, nil
}

// Apply applies one Secret manifest server-side as FieldManager. It never
// forces a conflict: a key another manager owns stops the apply.
func (l *Local) Apply(ctx context.Context, manifest []byte) error {
	_, stderr, err := l.run(ctx, manifest, l.args("apply", "--server-side", "--field-manager="+FieldManager, "-f", "-")...)
	if err != nil {
		return fmt.Errorf("kubectl apply: %w: %s", err, scrub(stderr, manifest))
	}
	return nil
}

func firstLine(b []byte) string {
	s, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return s
}

// scrub removes every value of the manifest from kubectl's message, in
// base64 and decoded, so an error never carries a value.
func scrub(msg, manifest []byte) string {
	var m struct {
		Data map[string]string `yaml:"data"`
	}
	s := strings.TrimSpace(string(msg))
	if err := yaml.Unmarshal(manifest, &m); err != nil {
		return "(message withheld: the manifest could not be read to scrub it)"
	}
	for _, enc := range m.Data {
		if enc == "" {
			continue
		}
		s = strings.ReplaceAll(s, enc, "[value withheld]")
		if dec, err := base64.StdEncoding.DecodeString(enc); err == nil && len(dec) > 0 {
			s = strings.ReplaceAll(s, string(dec), "[value withheld]")
		}
	}
	return s
}
