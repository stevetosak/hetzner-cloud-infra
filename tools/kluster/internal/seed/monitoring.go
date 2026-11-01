package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

// prometheusAPI is the Prometheus HTTP API through the API server's service
// proxy: no port-forward, and only the kubeconfig the other Stages use.
const prometheusAPI = "/api/v1/namespaces/monitoring/services/kps-prometheus:http-web/proxy/api/v1/"

// Monitoring is the health test of a rebuilt cluster (ADR 0011): every
// active scrape target is up, the Watchdog fires (so rules are evaluated),
// and no alert that would page on live fires or is pending. The live routing
// sends every alert to Telegram but the Watchdog, the InfoInhibitor and the
// info alerts (they stay in Grafana), so only those three do not count; an
// alert with no severity counts, as it pages on live. It only reads, and
// waits until the cluster settles or the timeout.
//
// A rehearsal only, after the database: the CNPG rules fire while no
// database Cluster exists.
type Monitoring struct {
	Waiting

	problems []string
}

func (*Monitoring) Name() string    { return "monitoring-clean" }
func (*Monitoring) Runbook() string { return Runbook + "#monitoring-clean" }

func (m *Monitoring) Probe(ctx context.Context, h *stage.Host) (stage.Status, error) {
	targets, ready, err := readPrometheus(ctx, h, "targets?state=active")
	if err != nil || !ready {
		return notReadYet, err
	}
	alerts, ready, err := readPrometheus(ctx, h, "alerts")
	if err != nil || !ready {
		return notReadYet, err
	}
	n, problems, err := monitoringProblems([]byte(targets), []byte(alerts))
	if err != nil {
		return stage.Status{}, err
	}
	m.problems = problems
	if len(problems) == 0 {
		return stage.Status{Done: true, Detail: fmt.Sprintf("%d target(s) up, the Watchdog fires, no alert that pages", n)}, nil
	}
	return stage.Status{Detail: summarize(problems, 5)}, nil
}

func (m *Monitoring) Preview(context.Context, *stage.Host) (string, error) {
	return fmt.Sprintf("read only: wait up to %s until every target is up and no alert that pages fires\n", m.Timeout), nil
}

func (m *Monitoring) Act(ctx context.Context, h *stage.Host) error {
	st, err := m.Until(ctx, func() (stage.Status, error) { return m.Probe(ctx, h) })
	if err != nil {
		return err
	}
	if !st.Done {
		return fmt.Errorf("monitoring is not clean after %s:\n  %s", m.Timeout, strings.Join(m.problems, "\n  "))
	}
	return nil
}

var notReadYet = stage.Status{Detail: "Prometheus cannot be read yet"}

// prometheusNotReady holds the kubectl messages of a Prometheus that is not
// there or does not serve yet: its Service or namespace is missing, it has
// no ready endpoint, or the proxy cannot reach it. Any other failure (RBAC,
// the kubeconfig, an API server that cannot be reached) is a fault: waiting
// for it until the timeout would hide its cause.
var prometheusNotReady = []string{
	"(NotFound)",
	"(ServiceUnavailable)",
	"no endpoints available",
	"error trying to reach service",
}

// readPrometheus reads one path of the Prometheus API. ready is false while
// Prometheus is not there or does not serve yet.
func readPrometheus(ctx context.Context, h *stage.Host, path string) (body string, ready bool, err error) {
	out, err := h.Exec.Run(ctx, adminEnv+"kubectl get --raw '"+prometheusAPI+path+"'")
	if err == nil {
		return out, true, nil
	}
	if ctx.Err() != nil {
		return "", false, ctx.Err()
	}
	for _, m := range prometheusNotReady {
		if strings.Contains(out, m) {
			return "", false, nil
		}
	}
	return "", false, fmt.Errorf("reading Prometheus %s: %w", path, err)
}

// pagesNobody holds the alerts that the live routing does not send to
// Telegram: the Watchdog (healthchecks.io), the InfoInhibitor and the info
// alerts (null).
func pagesNobody(labels map[string]string) bool {
	switch labels["alertname"] {
	case "Watchdog", "InfoInhibitor":
		return true
	}
	return labels["severity"] == "info"
}

// monitoringProblems reads the answers of /targets and /alerts and returns
// the number of active targets and every reason the cluster is not clean.
func monitoringProblems(targets, alerts []byte) (int, []string, error) {
	var t struct {
		Data struct {
			ActiveTargets []struct {
				ScrapePool string            `json:"scrapePool"`
				Labels     map[string]string `json:"labels"`
				Health     string            `json:"health"`
				LastError  string            `json:"lastError"`
			} `json:"activeTargets"`
		} `json:"data"`
	}
	if err := json.Unmarshal(targets, &t); err != nil {
		return 0, nil, fmt.Errorf("reading the Prometheus targets: %w", err)
	}
	var a struct {
		Data struct {
			Alerts []struct {
				Labels map[string]string `json:"labels"`
				State  string            `json:"state"`
			} `json:"alerts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(alerts, &a); err != nil {
		return 0, nil, fmt.Errorf("reading the Prometheus alerts: %w", err)
	}

	var problems []string
	if len(t.Data.ActiveTargets) == 0 {
		problems = append(problems, "no active target: nothing is scraped yet")
	}
	for _, x := range t.Data.ActiveTargets {
		if x.Health != "up" {
			p := fmt.Sprintf("target %s %s is %s", x.ScrapePool, x.Labels["instance"], x.Health)
			if x.LastError != "" {
				p += ": " + x.LastError
			}
			problems = append(problems, p)
		}
	}
	watchdog := false
	for _, x := range a.Data.Alerts {
		name := x.Labels["alertname"]
		if name == "Watchdog" && x.State == "firing" {
			watchdog = true
		}
		if !pagesNobody(x.Labels) {
			severity := x.Labels["severity"]
			if severity == "" {
				severity = "no severity"
			}
			p := fmt.Sprintf("alert %s (%s) is %s", name, severity, x.State)
			if ns := x.Labels["namespace"]; ns != "" {
				p += " in " + ns
			}
			problems = append(problems, p)
		}
	}
	if !watchdog {
		problems = append(problems, "the Watchdog does not fire: the rules are not evaluated")
	}
	slices.Sort(problems)
	return len(t.Data.ActiveTargets), slices.Compact(problems), nil
}

// summarize joins the first n items and counts the rest.
func summarize(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, "; ")
	}
	return fmt.Sprintf("%s; and %d more", strings.Join(items[:n], "; "), len(items)-n)
}
