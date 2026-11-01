package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/local"
	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// LiveLink is one way a rehearsal could act on the live outside world, and
// the read of the running cluster that shows its cut holds.
type LiveLink struct {
	What string
	// Get is the object kubectl reads: kind, namespace ("" when
	// cluster-scoped) and name. -o name only for a Secret. An empty Name
	// reads every object of the kind, by name: found is true when any exists.
	Kind, Namespace, Name string
	// Holds reports whether the cut holds, given the object's JSON (or its
	// name, for a Secret); found is false when there is no such object.
	Holds func(obj []byte, found bool, c *Cuts) (bool, error)
}

// Cut is the rehearsal cut of one core directory (core/rehearsal/<Dir>) and
// the live links it removes. A cut that removes none says why it exists.
type Cut struct {
	Dir   string
	Links []LiveLink
	Why   string
}

// RehearsalCuts lists every rehearsal cut. A test fails when a directory in
// core/rehearsal has no entry here (ADR 0009, Amendment 2026-10-25).
var RehearsalCuts = []Cut{
	{Dir: "argocd", Links: []LiveLink{
		{What: "a deploy notification (Telegram, the deploy catalog): the notifications controller runs",
			Kind: "deployments.apps", Namespace: "argocd", Name: "argocd-notifications-controller", Holds: zeroReplicas},
		{What: "a deploy notification: the notifications Secret exists",
			Kind: "secrets", Namespace: "argocd", Name: "argocd-notifications-secret", Holds: absent},
		{What: "an app deploy: the projects ApplicationSet exists",
			Kind: "applicationsets.argoproj.io", Namespace: "argocd", Name: "projects", Holds: absent},
	}},
	{Dir: "cert-manager", Links: []LiveLink{
		{What: "a real certificate: ClusterIssuer letsencrypt-prod asks Let's Encrypt (rate limits shared with live)",
			Kind: "clusterissuers.cert-manager.io", Name: "letsencrypt-prod", Holds: noACME},
		{What: "a DNS write: the live Cloudflare token is in the cluster",
			Kind: "secrets", Namespace: "cert-manager", Name: "cloudflare-dns-token", Holds: absent},
	}},
	{Dir: "cnpg", Links: []LiveLink{
		{What: "a write to the live backups: ObjectStore tosak-pg-backups does not archive to the rehearsal bucket",
			Kind: "objectstores.barmancloud.cnpg.io", Namespace: "pg-cluster", Name: "tosak-pg-backups", Holds: rehearsalBucket},
	}},
	{Dir: "monitoring", Links: []LiveLink{
		{What: "an alert to Telegram or a ping of the live Watchdog check: the Secret monitoring/alertmanager-receivers exists",
			Kind: "secrets", Namespace: "monitoring", Name: "alertmanager-receivers", Holds: absent},
		{What: "an alert to Telegram or a ping of the live Watchdog check: Alertmanager mounts alertmanager-receivers",
			Kind: "alertmanagers.monitoring.coreos.com", Namespace: "monitoring", Name: "kps-alertmanager", Holds: noReceiversMount},
		{What: "a probe of the live hosts: a Probe exists in monitoring",
			Kind: "probes.monitoring.coreos.com", Namespace: "monitoring", Holds: absent},
	}},
	{Dir: "reloader", Why: "drops Reloader's Roles in the projects' namespaces, which a rehearsal does not make; no live link"},
}

// Cuts is done when no live link remains in the running cluster. It only
// reads; while a link remains the rehearsal stops (a rehearsal only).
type Cuts struct {
	// Bucket is the rehearsal archive bucket.
	Bucket string

	broken []string
}

func (*Cuts) Name() string    { return "rehearsal-cuts" }
func (*Cuts) Runbook() string { return Runbook + "#rehearsal-cuts" }

func (c *Cuts) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	c.broken = nil
	n := 0
	for _, cut := range RehearsalCuts {
		for _, l := range cut.Links {
			n++
			obj, found, err := get(ctx, h, l)
			if err != nil {
				return stage.Status{}, err
			}
			ok, err := l.Holds(obj, found, c)
			if err != nil {
				return stage.Status{}, fmt.Errorf("%s: %w", l.What, err)
			}
			if !ok {
				c.broken = append(c.broken, "core/rehearsal/"+cut.Dir+": "+l.What)
			}
		}
	}
	if len(c.broken) == 0 {
		return stage.Status{Done: true, Detail: fmt.Sprintf("no live link: %d cut(s) hold", n)}, nil
	}
	return stage.Status{Detail: fmt.Sprintf("%d live link(s) remain", len(c.broken))}, nil
}

func (c *Cuts) Preview(context.Context, *stage.Host) (string, error) {
	if len(c.broken) == 0 {
		return "read only: show from the cluster that each rehearsal cut holds\n", nil
	}
	return "live links:\n  " + strings.Join(c.broken, "\n  ") + "\n", nil
}

func (c *Cuts) Act(context.Context, *stage.Host) error {
	return fmt.Errorf("the rehearsal can act on something live (the links above): it stops here; kluster down clears it away")
}

// get reads one object; found is false when it does not exist.
func get(ctx context.Context, h *stage.Host, l LiveLink) ([]byte, bool, error) {
	args := []string{"get", l.Kind}
	if l.Name != "" {
		args = append(args, l.Name)
	}
	args = append(args, "--ignore-not-found")
	if l.Namespace != "" {
		args = append(args, "-n", l.Namespace)
	}
	// A Secret is read by name only: it holds values. A whole kind too.
	if l.Kind == "secrets" || l.Name == "" {
		args = append(args, "-o", "name")
	} else {
		args = append(args, "-o", "json")
	}
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = local.Quote(a)
	}
	out, err := h.Exec.Run(ctx, adminEnv+"kubectl "+strings.Join(q, " "))
	if err != nil {
		return nil, false, err
	}
	out = strings.TrimSpace(out)
	return []byte(out), out != "", nil
}

func absent(_ []byte, found bool, _ *Cuts) (bool, error) { return !found, nil }

func zeroReplicas(obj []byte, found bool, _ *Cuts) (bool, error) {
	if !found {
		return true, nil
	}
	var d struct {
		Spec struct {
			Replicas *int `json:"replicas"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(obj, &d); err != nil {
		return false, err
	}
	return d.Spec.Replicas != nil && *d.Spec.Replicas == 0, nil
}

func noACME(obj []byte, found bool, _ *Cuts) (bool, error) {
	if !found {
		return true, nil
	}
	var i struct {
		Spec map[string]json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(obj, &i); err != nil {
		return false, err
	}
	_, acme := i.Spec["acme"]
	return !acme, nil
}

// noReceiversMount holds when the Alertmanager mounts no Secret
// alertmanager-receivers, whose files the live config sends with.
func noReceiversMount(obj []byte, found bool, _ *Cuts) (bool, error) {
	if !found {
		return true, nil
	}
	var a struct {
		Spec struct {
			Secrets []string `json:"secrets"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(obj, &a); err != nil {
		return false, err
	}
	return !slices.Contains(a.Spec.Secrets, "alertmanager-receivers"), nil
}

func rehearsalBucket(obj []byte, found bool, c *Cuts) (bool, error) {
	if !found {
		return true, nil
	}
	var s struct {
		Spec struct {
			Configuration struct {
				DestinationPath string `json:"destinationPath"`
			} `json:"configuration"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(obj, &s); err != nil {
		return false, err
	}
	return c.Bucket != "" && s.Spec.Configuration.DestinationPath == "s3://"+c.Bucket+"/", nil
}
