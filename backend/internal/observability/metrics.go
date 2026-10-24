package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics owns a private registry; no collectors use the global registry.
type Metrics struct {
	Registry     *prometheus.Registry
	requests     *prometheus.CounterVec
	duration     *prometheus.HistogramVec
	inflight     prometheus.Gauge
	calls        *prometheus.CounterVec
	callDuration *prometheus.HistogramVec
}

func NewMetrics() *Metrics {
	m := &Metrics{Registry: prometheus.NewRegistry()}
	labels := []string{"method", "route", "status"}
	m.requests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "cartside_http_requests_total", Help: "Completed API requests."}, labels)
	m.duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "cartside_http_request_duration_seconds", Help: "API completion duration in seconds.", Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2, 3, 5, 10}}, labels)
	m.inflight = prometheus.NewGauge(prometheus.GaugeOpts{Name: "cartside_http_requests_in_flight", Help: "Active API requests."})
	labels = []string{"operation", "provider", "outcome"}
	m.calls = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "cartside_assistant_calls_total", Help: "Completed outbound assistant calls."}, labels)
	m.callDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "cartside_assistant_call_duration_seconds", Help: "Outbound assistant completion duration in seconds.", Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 1.5, 2, 3}}, labels)
	m.Registry.MustRegister(m.requests, m.duration, m.inflight, m.calls, m.callDuration, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}
func (m *Metrics) AssistantCompleted(outcome string, duration time.Duration) {
	if m == nil {
		return
	}
	switch outcome {
	case "success", "request_rejected", "timeout", "cancelled", "rate_limited", "invalid_provider_output", "provider_unavailable", "invalid_input", "payload_too_large":
	default:
		outcome = "provider_unavailable"
	}
	m.calls.WithLabelValues("shopping_completion", "mock", outcome).Inc()
	m.callDuration.WithLabelValues("shopping_completion", "mock", outcome).Observe(duration.Seconds())
}
func (m *Metrics) completed(method, route string, status int, duration time.Duration) {
	label := "interrupted"
	if status >= 100 && status <= 599 {
		label = strconv.Itoa(status/100) + "xx"
	}
	m.requests.WithLabelValues(safeMethod(method), route, label).Inc()
	m.duration.WithLabelValues(safeMethod(method), route, label).Observe(duration.Seconds())
}
