package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/env"
)

// The cloud controller gets the cluster's own token; without one there is
// no Control Plane build, not a fallback to the Terraform token.
func TestCPStagesNeedTheClusterToken(t *testing.T) {
	cfg, err := config.Load("../kluster.yaml")
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{
		"TF_VAR_HCLOUD_TOKEN":   "terraform-token",
		"AWS_ACCESS_KEY_ID":     "key",
		"AWS_SECRET_ACCESS_KEY": "secret",
	}
	e, err := env.Resolve(context.Background(), cfg, config.EnvLive, func(k string) string { return vars[k] }, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{cfg: cfg, env: e}
	if _, err := a.cpStages(&cloud.Server{}); err == nil || !strings.Contains(err.Error(), "hcloud_cluster_token") {
		t.Errorf("err = %v, want a missing hcloud_cluster_token", err)
	}
	if err := cpInit(context.Background(), a); err == nil || !strings.Contains(err.Error(), "hcloud_cluster_token") {
		t.Errorf("cpInit err = %v, want a missing hcloud_cluster_token before any API call", err)
	}
}
