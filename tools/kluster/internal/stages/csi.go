package stages

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// The hcloud CSI driver's StorageClasses (core/hcloud/storageclasses.yaml).
const (
	defaultClass = "hcloud-volumes"
	retainClass  = "hcloud-volumes-retain"
	locationKey  = "csi.hetzner.cloud/location"
	providedBy   = "instance.hetzner.cloud/provided-by"
)

// csiClasses are the classes the Stage applies and checks, the default first.
var csiClasses = []string{defaultClass, retainClass}

// CSI is the hcloud CSI driver and its two StorageClasses (ADR 0005,
// ADR 0016). The node pod runs only on Hetzner servers (provided-by=cloud),
// and each class allows only the location of the hcloud site, so no claim is
// provisioned for a Node without the driver. Its controller stays Pending
// until a Worker joins, by design; only the node DaemonSet is checked.
//
// Act applies both files: kubectl apply changes allowedTopologies of an
// existing class in place, and nothing here deletes a class.
type CSI struct {
	Manifest       string // core/hcloud/csi.yaml
	StorageClasses string // core/hcloud/storageclasses.yaml
	Location       string // the hcloud site's name: its Hetzner location
}

func (CSI) Name() string    { return "csi" }
func (CSI) Runbook() string { return "docs/runbook/control-plane.md#8c-the-csi-driver" }

func (CSI) script() string {
	return adminKubectl + `kubectl apply -f ` + manifestDir + `/csi.yaml
kubectl apply -f ` + manifestDir + `/storageclasses.yaml
kubectl -n kube-system rollout status ds/hcloud-csi-node --timeout=300s
`
}

func (c CSI) checks() []check {
	checks := []check{
		{"default-storageclass", adminKubectl + `[ "$(kubectl get sc ` + defaultClass + ` -o jsonpath='{.metadata.annotations.storageclass\.kubernetes\.io/is-default-class}')" = true ]`},
	}
	for _, class := range csiClasses {
		checks = append(checks, check{"topology-" + class, adminKubectl +
			`[ "$(kubectl get sc ` + class + ` -o jsonpath='{.allowedTopologies[*].matchLabelExpressions[?(@.key=="` + locationKey + `")].values[*]}')" = ` + shellWord(c.Location) + ` ]`})
	}
	return append(checks,
		check{"csi-node-affinity", adminKubectl +
			`[ "$(kubectl -n kube-system get ds hcloud-csi-node -o jsonpath='{.spec.template.spec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[*].matchExpressions[?(@.key=="` + providedBy + `")].operator}/{.spec.template.spec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[*].matchExpressions[?(@.key=="` + providedBy + `")].values[*]}')" = In/cloud ]`},
		check{"csi-node", adminKubectl + "kubectl -n kube-system rollout status ds/hcloud-csi-node --timeout=5s"},
	)
}

// shellWord quotes s for a test word; an empty Location must not vanish.
func shellWord(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (c CSI) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	return probe(ctx, h, c.checks())
}

func (c CSI) Preview(context.Context, *stage.Host) (string, error) {
	return "# write core/hcloud/csi.yaml and core/hcloud/storageclasses.yaml to " + manifestDir + "\n" + c.script(), nil
}

// Act refuses manifests that disagree with the hcloud site before it writes
// anything.
func (c CSI) Act(ctx context.Context, h *stage.Host) error {
	if err := c.checkManifests(); err != nil {
		return err
	}
	if err := writeManifest(ctx, h, "csi", c.Manifest); err != nil {
		return err
	}
	if err := writeManifest(ctx, h, "storageclasses", c.StorageClasses); err != nil {
		return err
	}
	_, err := runScript(ctx, h, c.Name(), c.script())
	return err
}

// storageClass is the part of a StorageClass the Stage reads.
type storageClass struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name        string            `yaml:"name"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
	AllowedTopologies []struct {
		MatchLabelExpressions []struct {
			Key    string   `yaml:"key"`
			Values []string `yaml:"values"`
		} `yaml:"matchLabelExpressions"`
	} `yaml:"allowedTopologies"`
}

// locations is every value the class allows for the location key.
func (s storageClass) locations() []string {
	var out []string
	for _, term := range s.AllowedTopologies {
		for _, e := range term.MatchLabelExpressions {
			if e.Key == locationKey {
				out = append(out, e.Values...)
			}
		}
	}
	return out
}

// checkManifests holds csi.yaml and storageclasses.yaml to the hcloud site:
// csi.yaml carries no StorageClass (one writer per class), and
// storageclasses.yaml holds both classes, each allowing only Location, with
// hcloud-volumes the default.
func (c CSI) checkManifests() error {
	if c.Location == "" {
		return errors.New("csi: no hcloud site location to allow")
	}
	inCSI, err := storageClasses(c.Manifest)
	if err != nil {
		return fmt.Errorf("core/hcloud/csi.yaml: %w", err)
	}
	if len(inCSI) > 0 {
		return fmt.Errorf("core/hcloud/csi.yaml carries a StorageClass; csi-values.yaml must set storageClasses: []")
	}
	classes, err := storageClasses(c.StorageClasses)
	if err != nil {
		return fmt.Errorf("core/hcloud/storageclasses.yaml: %w", err)
	}
	for _, name := range csiClasses {
		class, ok := classes[name]
		if !ok {
			return fmt.Errorf("core/hcloud/storageclasses.yaml has no StorageClass %s", name)
		}
		if got := class.locations(); !slices.Equal(got, []string{c.Location}) {
			return fmt.Errorf("core/hcloud/storageclasses.yaml: %s allows %s %v, but the hcloud site is %s", name, locationKey, got, c.Location)
		}
	}
	if classes[defaultClass].Metadata.Annotations["storageclass.kubernetes.io/is-default-class"] != "true" {
		return fmt.Errorf("core/hcloud/storageclasses.yaml: %s is not the default class", defaultClass)
	}
	return nil
}

// storageClasses reads every StorageClass of a multi-document manifest by name.
func storageClasses(manifest string) (map[string]storageClass, error) {
	out := map[string]storageClass{}
	dec := yaml.NewDecoder(strings.NewReader(manifest))
	for {
		var doc storageClass
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if doc.Kind == "StorageClass" {
			out[doc.Metadata.Name] = doc
		}
	}
}
