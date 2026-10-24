package observability_test

import (
	"context"
	"github.com/WilliamSchweitzer/cartside/backend/internal/aiprotocol"
	"github.com/WilliamSchweitzer/cartside/backend/internal/observability"
	"github.com/WilliamSchweitzer/cartside/backend/internal/shoppingassistant"
	"github.com/go-chi/chi/v5"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Transport-only fixture: production auth and shopping are covered separately.
func TestMonitoringStack(t *testing.T) {
	folder := os.Getenv("MONITORING_FIXTURE_DIR")
	if folder == "" {
		t.Skip("isolated monitoring runner required")
	}
	m := observability.NewMetrics()
	c, err := shoppingassistant.NewClient(os.Getenv("MONITORING_GATEWAY")+"/internal/ai/chat/completions", os.Getenv("CARTSIDE_AI_SERVICE_KEY"), m)
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(observability.Middleware("api", observability.ChiRoute, m))
	r.Get("/health/live", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	r.Post("/assistant/messages", func(w http.ResponseWriter, r *http.Request) {
		_, err := c.Complete(r.Context(), aiprotocol.Input{Version: 1, Scenario: "breakfast for two", Candidates: []aiprotocol.Candidate{}})
		if err != nil {
			w.WriteHeader(502)
		}
	})
	for _, item := range []struct {
		addr    string
		handler http.Handler
	}{{os.Getenv("MONITORING_API_ADDR"), r}, {os.Getenv("MONITORING_METRICS_ADDR"), m.Handler()}} {
		l, e := net.Listen("tcp", item.addr)
		if e != nil {
			t.Fatal(e)
		}
		s := &http.Server{Handler: item.handler, ReadHeaderTimeout: time.Second}
		go s.Serve(l)
		defer s.Shutdown(context.Background())
	}
	os.WriteFile(filepath.Join(folder, "ready"), []byte("ready"), 0600)
	deadline := time.Now().Add(8 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(folder, "stop")); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("monitoring runner timed out")
}
