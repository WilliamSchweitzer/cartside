package observability

import (
	"context"
	"fmt"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPMetricsBoundaries(t *testing.T) {
	m := NewMetrics()
	r := chi.NewRouter()
	r.Use(Middleware("api", ChiRoute, m))
	r.Get("/products/{id}", func(w http.ResponseWriter, r *http.Request) {
		families, _ := m.Registry.Gather()
		for _, f := range families {
			if f.GetName() == "cartside_http_requests_in_flight" && f.Metric[0].Gauge.GetValue() != 1 {
				t.Error("missing inflight")
			}
		}
		w.WriteHeader(503)
	})
	for i := 0; i < 40; i++ {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", fmt.Sprintf("/unknown-sensitive-%d?q=private", i), nil))
	}
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/products/private-product?q=secret", nil))
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	b := w.Body.String()
	for _, v := range []string{`cartside_http_requests_total{method="GET",route="unmatched",status="4xx"} 40`, `cartside_http_request_duration_seconds_count{method="GET",route="/products/{id}",status="5xx"} 1`, `cartside_http_requests_in_flight 0`} {
		if !strings.Contains(b, v) {
			t.Errorf("missing %s", v)
		}
	}
	for _, v := range []string{"private", "secret", "unknown-sensitive"} {
		if strings.Contains(b, v) {
			t.Error("sensitive value in metrics")
		}
	}
	other := NewMetrics()
	families, _ := other.Registry.Gather()
	for _, f := range families {
		if f.GetName() == "cartside_http_requests_total" {
			t.Error("registry isolation failed")
		}
	}
}
func TestInterruptedHTTPMetrics(t *testing.T) {
	m := NewMetrics()
	h := Middleware("api", func(*http.Request) string { return "/panic" }, m)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("fixture") }))
	func() {
		defer func() { _ = recover() }()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/panic", nil))
	}()
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(w.Body.String(), `cartside_http_requests_total{method="GET",route="/panic",status="interrupted"} 1`) {
		t.Fatal(w.Body.String())
	}
}

func TestCancelledHTTPMetrics(t *testing.T) {
	m := NewMetrics()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := Middleware("api", func(*http.Request) string { return "/cancel" }, m)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Err() != context.Canceled {
			t.Error("expected cancellation")
		}
		w.WriteHeader(499)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/cancel", nil).WithContext(ctx))
	families, _ := m.Registry.Gather()
	for _, f := range families {
		if f.GetName() == "cartside_http_requests_total" && f.Metric[0].Counter.GetValue() != 1 {
			t.Error("completion count")
		}
		if f.GetName() == "cartside_http_request_duration_seconds" && (f.Metric[0].Histogram.GetSampleCount() != 1 || f.Metric[0].Histogram.GetSampleSum() <= 0) {
			t.Error("duration missing")
		}
	}
}
