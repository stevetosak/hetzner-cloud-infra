package kube

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/catalog"
)

func TestLocalNamesItsContext(t *testing.T) {
	var got []string
	var stdin string
	run := func(_ context.Context, in []byte, args ...string) ([]byte, []byte, error) {
		got, stdin = args, string(in)
		return nil, nil, nil
	}
	l := NewLocalWithRunner("/home/op/.kube/config", ContextName("tosak"), run)
	ref := catalog.Ref{Namespace: "redis", Name: "redis"}

	out, err := l.Secret(context.Background(), ref)
	if err != nil || out != nil {
		t.Fatalf("absent Secret: %q, %v", out, err)
	}
	want := []string{"--kubeconfig", "/home/op/.kube/config", "--context", "tosak-admin@tosak",
		"get", "secret", "-n", "redis", "redis", "-o", "yaml", "--ignore-not-found"}
	if !slices.Equal(got, want) {
		t.Errorf("get args %q", got)
	}

	if err := l.Apply(context.Background(), []byte("kind: Secret\n")); err != nil {
		t.Fatal(err)
	}
	want = []string{"--kubeconfig", "/home/op/.kube/config", "--context", "tosak-admin@tosak",
		"apply", "--server-side", "--field-manager=kluster", "-f", "-"}
	if !slices.Equal(got, want) || stdin != "kind: Secret\n" {
		t.Errorf("apply args %q, stdin %q", got, stdin)
	}
	if slices.Contains(got, "--force-conflicts") {
		t.Error("apply forces conflicts")
	}
}

func TestApplyErrorHoldsNoValue(t *testing.T) {
	// "aHVudGVyMg==" is "hunter2".
	manifest := []byte("kind: Secret\ndata:\n  password: aHVudGVyMg==\n")
	run := func(context.Context, []byte, ...string) ([]byte, []byte, error) {
		return nil, []byte("invalid value aHVudGVyMg== near hunter2: conflict with \"secrets-sh\": .data.password"), errors.New("exit status 1")
	}
	err := NewLocalWithRunner("k", "c", run).Apply(context.Background(), manifest)
	if err == nil || strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "aHVudGVyMg==") {
		t.Fatalf("error %v", err)
	}
	if !strings.Contains(err.Error(), ".data.password") {
		t.Errorf("error lost the key name: %v", err)
	}
}
