// Package observability provides bounded, payload-free request correlation.
package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const Header = "X-Request-ID"

type contextKey struct{}

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func ValidID(id string) bool        { return validID.MatchString(id) }
func ID(ctx context.Context) string { id, _ := ctx.Value(contextKey{}).(string); return id }
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("request ID entropy unavailable")
	}
	return hex.EncodeToString(b[:])
}

// Middleware records only route templates; a supplied safe ID conveys no identity.
func Middleware(component string, route func(*http.Request) string, metrics ...*Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			var metric *Metrics
			if len(metrics) > 0 {
				metric = metrics[0]
			}
			if metric != nil {
				metric.inflight.Inc()
				defer metric.inflight.Dec()
			}
			ids := r.Header.Values(Header)
			id := ""
			if len(ids) == 1 && ValidID(ids[0]) {
				id = ids[0]
			}
			if id == "" {
				id = newID()
			}
			r = r.WithContext(WithID(r.Context(), id))
			w.Header().Set(Header, id)
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			completed := false
			defer func() {
				status := ww.Status()
				if status == 0 && completed {
					status = 200
				}
				label := route(r)
				if label == "" {
					label = "unmatched"
				}
				if metric != nil {
					metric.completed(r.Method, label, status, time.Since(start))
				}
				category := "none"
				switch {
				case !completed:
					category = "interrupted"
				case status == 429:
					category = "rate_limited"
				case status >= 500:
					category = "server_error"
				case status >= 400:
					category = "request_rejected"
				}
				slog.InfoContext(r.Context(), "request_completed", "component", component, "request_id", id, "method", safeMethod(r.Method), "route", label, "status", status, "duration_ms", float64(time.Since(start).Microseconds())/1000, "error_category", category)
			}()
			next.ServeHTTP(ww, r)
			completed = true
		})
	}
}
func safeMethod(m string) string {
	switch m {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD":
		return m
	}
	return "OTHER"
}
func ChiRoute(r *http.Request) string { return chi.RouteContext(r.Context()).RoutePattern() }
