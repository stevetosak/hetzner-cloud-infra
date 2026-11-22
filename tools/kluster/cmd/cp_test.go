package cmd

import (
	"context"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/bitwarden/fakevault"
	"strings"
	"testing"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/cloud"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/config"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/env"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stages"
)

// The cloud controller gets the cluster's own token; without one there is
// no Control Plane build, not a fallback to the Terraform token.
func TestCPStagesNeedTheClusterToken(t *testing.T) {
	cfg, err := config.Load("../kluster.yaml")
	if err != nil {
		t.Fatal(err)
	}
	e, err := env.Resolve(context.Background(), cfg, config.EnvLive, fakevault.Of(map[string][]string{"kluster/live": {"hcloud_token=live-token", "r2_access_key_id=live-key", "r2_secret_access_key=live-secret"}}))
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

// The CSI Stage gets both of its files and the hcloud site's location, so
// cp init applies the classes it checks.
func TestCPStagesGiveCSIItsFilesAndTheSite(t *testing.T) {
	cfg, err := config.Load("../kluster.yaml")
	if err != nil {
		t.Fatal(err)
	}
	e, err := env.Resolve(context.Background(), cfg, config.EnvLive, fakevault.Of(map[string][]string{"kluster/live": {"hcloud_token=live-token", "hcloud_cluster_token=cluster-token", "r2_access_key_id=live-key", "r2_secret_access_key=live-secret"}}))
	if err != nil {
		t.Fatal(err)
	}
	all, err := (&app{cfg: cfg, env: e}).cpStages(&cloud.Server{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if csi, ok := s.(stages.CSI); ok {
			if csi.Location != "hel1" || !strings.Contains(csi.StorageClasses, "name: hcloud-volumes-retain") || !strings.Contains(csi.Manifest, "name: hcloud-csi-node") {
				t.Fatalf("CSI Stage = location %q, classes %d bytes, manifest %d bytes", csi.Location, len(csi.StorageClasses), len(csi.Manifest))
			}
			return
		}
	}
	t.Fatal("no CSI Stage")
}
