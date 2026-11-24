package nodecontroller

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics are what core/monitoring/rules/nodes.yaml reads.
type Metrics struct {
	registry   *prometheus.Registry
	actions    *prometheus.CounterVec
	held       *prometheus.GaugeVec
	removed    *prometheus.GaugeVec
	lastPass   prometheus.Gauge
	passErrors prometheus.Counter
}

// NewMetrics registers the controller's metrics, and the Go runtime's and
// the process's, on a registry of their own.
func NewMetrics() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		actions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "node_controller_actions_total",
			Help: "Steps taken on a Node: taint, untaint or delete, with the reason that allowed it.",
		}, []string{"action", "node", "reason"}),
		held: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "node_controller_held",
			Help: "1 while a Worker is due for removal and a guard holds it: volume-attached, mass-guard, paused or pause-invalid.",
		}, []string{"node", "reason"}),
		// A gauge, not the counter, carries the removal notice: a counter
		// series that starts at 1 shows no increase, so the first removal
		// after a restart of Prometheus or of the controller would be lost.
		removed: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "node_controller_removed_timestamp_seconds",
			Help: "When the controller deleted the Node, with the reason that allowed it.",
		}, []string{"node", "reason"}),
		lastPass: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "node_controller_last_pass_timestamp_seconds",
			Help: "When the last pass that ended without an error ran.",
		}),
		passErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "node_controller_pass_errors_total",
			Help: "Passes that ended with an error.",
		}),
	}
	m.registry.MustRegister(m.actions, m.held, m.removed, m.lastPass, m.passErrors,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// Handler serves /metrics, /health/live and /health. /health/live is the
// process only. /health fails until the caches have synced, and when no pass
// has ended without an error for stale.
func Handler(m *Metrics, synced func() bool, lastPass func() time.Time, stale time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		switch last := lastPass(); {
		case !synced():
			http.Error(w, "the caches have not synced", http.StatusServiceUnavailable)
		case last.IsZero():
			http.Error(w, "no pass has ended yet", http.StatusServiceUnavailable)
		case time.Since(last) > stale:
			http.Error(w, "no pass has ended without an error since "+last.UTC().Format(time.RFC3339), http.StatusServiceUnavailable)
		default:
			w.Write([]byte("ok\n"))
		}
	})
	return mux
}
