package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/auth"
	"github.com/WilliamSchweitzer/cartside/backend/internal/observability"
	"github.com/go-chi/chi/v5"
)

// NewRouter constructs the application's HTTP routes.
func NewRouter(checkReady func(context.Context) error, authenticate func(http.Handler) http.Handler, catalog Catalog, csrf CSRF, carts ...Cart) http.Handler {
	return newRouter(checkReady, authenticate, catalog, csrf, nil, nil, nil, nil, carts...)
}

// NewRouterWithCheckout wires checkout alongside the existing routes.
func NewRouterWithCheckout(checkReady func(context.Context) error, authenticate func(http.Handler) http.Handler, catalog Catalog, csrf CSRF, carts Cart, checkout Checkout) http.Handler {
	return newRouter(checkReady, authenticate, catalog, csrf, checkout, nil, nil, nil, carts)
}

func NewRouterWithAssistant(checkReady func(context.Context) error, authenticate func(http.Handler) http.Handler, catalog Catalog, csrf CSRF, carts Cart, checkout Checkout, assistant Assistant, limiter *AssistantLimiter, metrics ...*observability.Metrics) http.Handler {
	return newRouter(checkReady, authenticate, catalog, csrf, checkout, assistant, limiter, metrics, carts)
}

func newRouter(checkReady func(context.Context) error, authenticate func(http.Handler) http.Handler, catalog Catalog, csrf CSRF, checkout Checkout, assistant Assistant, limiter *AssistantLimiter, metrics []*observability.Metrics, carts ...Cart) http.Handler {
	r := chi.NewRouter()
	r.Use(observability.Middleware("api", observability.ChiRoute, metrics...))
	r.Use(func(next http.Handler) http.Handler {
		protected := authenticate(csrf.Middleware(next))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && (r.URL.Path == "/health/live" || r.URL.Path == "/health/ready") {
				next.ServeHTTP(w, r)
				return
			}
			if r.URL.Path == "/assistant/messages" {
				w.Header().Set("Cache-Control", "no-store")
				protected.ServeHTTP(&assistantBoundary{ResponseWriter: w}, r)
				return
			}
			if r.URL.Path == "/checkout" {
				protected.ServeHTTP(&checkoutAuthWriter{ResponseWriter: w}, r)
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
	if len(carts) > 0 {
		cartRoutes(r, carts[0])
	}
	if checkout != nil {
		r.Post("/checkout", checkoutHandler(checkout))
	}
	r.Post("/assistant/messages", assistantHandler(assistant, limiter))
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
