// Package metrics holds the Prometheus series exposed on the internal METRICS_ADDR listener
// (scraped by Grafana Alloy, decision IN3). Labels never carry ids, tokens or customer data.
package metrics

import (
	"database/sql"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// HTTPRequests counts finished requests by route template (never the raw path, which can hold QR/tracker tokens).
	HTTPRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mycaffe_http_requests_total",
		Help: "HTTP requests by method, route template and status code.",
	}, []string{"method", "route", "status"})

	HTTPDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "mycaffe_http_request_duration_seconds",
		Help:    "HTTP request latency by method and route template.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})

	// WorkerLastSuccess lets an alert fire when a background worker stops completing cycles.
	WorkerLastSuccess = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mycaffe_worker_last_success_timestamp_seconds",
		Help: "Unix time of the last completed cycle per background worker.",
	}, []string{"worker"})

	EmailSendFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "mycaffe_email_send_failures_total",
		Help: "Failed email deliveries; permanent=true means the email will not be retried.",
	}, []string{"permanent"})
)

// WorkerSucceeded records a completed worker cycle.
func WorkerSucceeded(worker string) {
	WorkerLastSuccess.WithLabelValues(worker).SetToCurrentTime()
}

// Handler serves the default registry plus connection-pool stats for db (nil skips them).
func Handler(db *sql.DB) http.Handler {
	if db != nil {
		prometheus.MustRegister(collectors.NewDBStatsCollector(db, "mycaffe"))
	}
	return promhttp.Handler()
}
