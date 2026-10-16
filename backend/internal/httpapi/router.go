package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/auth"
	"github.com/go-chi/chi/v5"
)

// NewRouter constructs the application's HTTP routes.
func NewRouter(checkReady func(context.Context) error, authenticate func(http.Handler) http.Handler, catalog Catalog) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		protected := authenticate(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && (r.URL.Path == "/health/live" || r.URL.Path == "/health/ready") {
				next.ServeHTTP(w, r)
				return
			}
			protected.ServeHTTP(w, r)
		})
	})
	r.Get("/health/live", live)
	r.Get("/health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "application/json")
		if err := checkReady(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	r.Get("/products", listProducts(catalog))
	r.Get("/products/{id}", getProduct(catalog))
	r.Get("/me", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		principal, ok := auth.FromContext(r.Context())
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(principal)
	})
	return r
}

func live(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
