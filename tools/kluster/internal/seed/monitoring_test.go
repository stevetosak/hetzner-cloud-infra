package seed

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stevetosak/hetzner-cloud-infra/tools/kluster/internal/stage"
)

const (
	targetsUp = `{"status":"success","data":{"activeTargets":[
		{"scrapePool":"serviceMonitor/monitoring/kps-apiserver/0","labels":{"instance":"10.0.0.2:6443"},"health":"up","lastError":""},
		{"scrapePool":"podMonitor/monitoring/argocd/0","labels":{"instance":"10.244.1.5:8082"},"health":"up","lastError":""}]}}`
	alertsClean = `{"status":"success","data":{"alerts":[
		{"labels":{"alertname":"Watchdog","severity":"none"},"state":"firing"},
		{"labels":{"alertname":"CPUThrottlingHigh","severity":"info","namespace":"redis"},"state":"firing"},
		{"labels":{"alertname":"InfoInhibitor","severity":"none"},"state":"firing"}]}}`
)

// prometheus answers the two raw reads of the monitoring-clean Stage.
type prometheus struct{ targets, alerts string }

func (p prometheus) Run(_ context.Context, cmd string) (string, error) {
	switch {
	case p.targets == "":
		return "", os.ErrNotExist
	case strings.Contains(cmd, "get --raw") && strings.Contains(cmd, "/targets?state=active"):
		return p.targets, nil
	case strings.Contains(cmd, "get --raw") && strings.Contains(cmd, "/alerts"):
		return p.alerts, nil
	}
	return "", os.ErrNotExist
}
func (prometheus) WriteFile(context.Context, string, string, os.FileMode) error { return nil }
func (prometheus) ReadFile(context.Context, string) (string, error)             { return "", nil }

// Clean: every target up and the Watchdog fires. Info alerts page nobody,
// so they do not count.
func TestMonitoringClean(t *testing.T) {
	m := &Monitoring{}
	st, err := m.Probe(context.Background(), &stage.Host{Name: "cp", Exec: prometheus{targetsUp, alertsClean}})
	if err != nil || !st.Done {
		t.Fatalf("%+v %v", st, err)
	}
	if !strings.Contains(st.Detail, "2 target(s) up") {
		t.Errorf("detail: %s", st.Detail)
	}
}

// Not clean: a target down, a warning pending, a critical firing, and no
// Watchdog. Each is named, and the Stage fails at its timeout.
func TestMonitoringNamesEveryProblem(t *testing.T) {
	targets := `{"data":{"activeTargets":[
		{"scrapePool":"podMonitor/monitoring/tosak-pg-cluster/0","labels":{"instance":"10.244.2.7:9187"},"health":"down","lastError":"connection refused"}]}}`
	alerts := `{"data":{"alerts":[
		{"labels":{"alertname":"KubePodNotReady","severity":"warning","namespace":"redis"},"state":"pending"},
		{"labels":{"alertname":"CNPGNoPrimary","severity":"critical"},"state":"firing"}]}}`
	m := &Monitoring{Waiting: Waiting{Timeout: time.Millisecond, Poll: time.Millisecond}}
	var out strings.Builder
	err := stage.Run(context.Background(), []stage.Stage{m}, &stage.Host{Name: "cp", Exec: prometheus{targets, alerts}},
		stage.Options{Apply: true, Out: &out})
	if err == nil {
		t.Fatal("a cluster with a target down passed")
	}
	for _, want := range []string{
		"target podMonitor/monitoring/tosak-pg-cluster/0 10.244.2.7:9187 is down: connection refused",
		"alert KubePodNotReady (warning) is pending in redis",
		"alert CNPGNoPrimary (critical) is firing",
		"the Watchdog does not fire",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("not named: %s\n%v", want, err)
		}
	}
}

// Before Prometheus runs, the reads fail: not done yet, not an error.
func TestMonitoringWaitsForPrometheus(t *testing.T) {
	m := &Monitoring{}
	st, err := m.Probe(context.Background(), &stage.Host{Name: "cp", Exec: prometheus{}})
	if err != nil || st.Done || !strings.Contains(st.Detail, "cannot be read yet") {
		t.Fatalf("%+v %v", st, err)
	}
}

// No target at all is not clean: nothing is scraped.
func TestMonitoringNeedsATarget(t *testing.T) {
	_, problems, err := monitoringProblems([]byte(`{"data":{"activeTargets":[]}}`), []byte(alertsClean))
	if err != nil || len(problems) != 1 || !strings.Contains(problems[0], "no active target") {
		t.Fatalf("%v %v", problems, err)
	}
}

// The monitoring test runs in a rehearsal only, after the database and
// before the zone.
func TestAfterDB(t *testing.T) {
	z := &Zone{}
	live := &Plan{Zone: z}
	if got := live.AfterDB(); len(got) != 1 || got[0] != stage.Stage(z) {
		t.Errorf("live: %v", got)
	}
	m := &Monitoring{}
	reh := &Plan{Zone: z, Monitoring: m}
	if got := reh.AfterDB(); len(got) != 2 || got[0] != stage.Stage(m) || got[1] != stage.Stage(z) {
		t.Errorf("rehearsal: %v", got)
	}
}
