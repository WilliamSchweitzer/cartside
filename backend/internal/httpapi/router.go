package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// NewRouter constructs the application's HTTP routes.
func NewRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/health/live", live)
	return r
}

func live(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
