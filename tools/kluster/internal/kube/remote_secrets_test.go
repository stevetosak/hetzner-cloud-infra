package kube

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
)

// cpExec answers each command with out and err, and keeps the files.
type cpExec struct {
	cmds  []string
	files map[string]string
	out   string
	err   error
}

func (s *cpExec) Run(_ context.Context, cmd string) (string, error) {
	s.cmds = append(s.cmds, cmd)
	if strings.HasPrefix(cmd, "mkdir") {
		return "", nil
	}
	return s.out, s.err
}

func (s *cpExec) WriteFile(_ context.Context, path, content string, mode os.FileMode) error {
	if mode != 0o600 {
		return errors.New("a Secret file must be 0600")
	}
	if s.files == nil {
		s.files = map[string]string{}
	}
	s.files[path] = content
	return nil
}

func (s *cpExec) ReadFile(context.Context, string) (string, error) { return "", nil }

const remoteManifest = "apiVersion: v1\nkind: Secret\nmetadata: {name: redis, namespace: redis}\ndata:\n  password: c2VjcmV0dmFsdWU=\n"

func TestRemoteApplyGoesThroughTmpfsAndScrubs(t *testing.T) {
	s := &cpExec{out: "Apply failed: conflict on secretvalue", err: errors.New("exit 1")}
	err := Kubectl{Exec: s}.Apply(context.Background(), []byte(remoteManifest), true)
	if err == nil {
		t.Fatal("a failed apply passed")
	}
	if strings.Contains(err.Error(), "secretvalue") || strings.Contains(err.Error(), "c2VjcmV0dmFsdWU=") {
		t.Errorf("the error holds the value: %v", err)
	}
	if s.files["/run/kluster/secret.yaml"] != remoteManifest {
		t.Errorf("files = %v", s.files)
	}
	last := s.cmds[len(s.cmds)-1]
	for _, want := range []string{"--server-side", "--field-manager=kluster", "--force-conflicts", "-f /run/kluster/secret.yaml", "rm -f /run/kluster/secret.yaml"} {
		if !strings.Contains(last, want) {
			t.Errorf("apply %q lacks %q", last, want)
		}
	}
}

func TestRemoteSecret(t *testing.T) {
	ref := catalog.Ref{Namespace: "redis", Name: "redis"}
	s := &cpExec{out: "  \n"}
	got, err := Kubectl{Exec: s}.Secret(context.Background(), ref)
	if err != nil || got != nil {
		t.Fatalf("absent Secret: %q, %v", got, err)
	}
	s = &cpExec{out: "secretvalue", err: errors.New("running ... (output: secretvalue)")}
	if _, err := (Kubectl{Exec: s}).Secret(context.Background(), ref); err == nil || strings.Contains(err.Error(), "secretvalue") {
		t.Errorf("a failed read: %v", err)
	}
}
