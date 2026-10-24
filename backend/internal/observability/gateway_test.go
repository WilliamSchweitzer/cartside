package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/WilliamSchweitzer/cartside/backend/internal/aiprotocol"
	"github.com/WilliamSchweitzer/cartside/backend/internal/observability"
	"github.com/WilliamSchweitzer/cartside/backend/internal/shoppingassistant"
	"github.com/go-chi/chi/v5"
)

// This disposable transport fixture exercises correlation, not authentication or
// shopping behavior. The production credential guard and AI route are unchanged.
func TestGatewayCorrelation(t *testing.T) {
	gateway := os.Getenv("CORRELATION_GATEWAY")
	if gateway == "" {
		t.Skip("isolated gateway required")
	}
	client, err := shoppingassistant.NewClient(gateway+"/internal/ai/chat/completions", os.Getenv("CORRELATION_SERVICE_KEY"))
	if err != nil {
		t.Fatal("client configuration")
	}
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	router := chi.NewRouter()
	router.Use(observability.Middleware("api", observability.ChiRoute))
	router.Get("/health/live", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	router.Post("/assistant/messages", func(w http.ResponseWriter, r *http.Request) {
		_, err := client.Complete(r.Context(), aiprotocol.Input{Version: 1, Scenario: "breakfast for two", Candidates: []aiprotocol.Candidate{}})
		if err != nil {
			w.WriteHeader(502)
			return
		}
		w.WriteHeader(200)
	})
	listener, err := net.Listen("tcp", ":"+os.Getenv("CORRELATION_API_PORT"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: router}
	go server.Serve(listener)
	defer server.Shutdown(context.Background())
	ids := []string{}
	for i, path := range []string{"/api/health/live", "/api/health/live", "/api/health/live", "/api/assistant/messages"} {
		method := "GET"
		if path == "/api/assistant/messages" {
			method = "POST"
		}
		req, _ := http.NewRequest(method, gateway+path, nil)
		supplied := []string{"browser-supplied-must-replace", "invalid/id", strings.Repeat("x", 512), "browser-supplied-must-replace"}[i]
		req.Header.Set(observability.Header, supplied)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal("gateway request failed")
		}
		res.Body.Close()
		id := res.Header.Get(observability.Header)
		if !observability.ValidID(id) || id == supplied {
			t.Fatal("gateway failed to replace ID")
		}
		ids = append(ids, id)
		expected := 200
		if method == "POST" && os.Getenv("CORRELATION_SCENARIO") != "normal" {
			expected = 502
		}
		if res.StatusCode != expected {
			t.Fatalf("status %d expected %d", res.StatusCode, expected)
		}
	}
	// Shutdown waits for completion logging before the buffer is inspected.
	server.Shutdown(context.Background())
	events := []map[string]any{}
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var e map[string]any
		if json.Unmarshal(line, &e) != nil {
			t.Fatal("non JSON event")
		}
		events = append(events, e)
	}
	if len(events) != 5 {
		t.Fatal("expected four incoming and one outbound event", len(events))
	}
	if events[0]["request_id"] != ids[0] || events[3]["request_id"] != ids[3] || events[4]["request_id"] != ids[3] {
		t.Fatal("Go correlation mismatch")
	}
	want := map[string]string{"normal": "success", "malformed": "invalid_provider_output", "rate-limit": "rate_limited", "delay": "timeout", "server-error": "provider_unavailable"}[os.Getenv("CORRELATION_SCENARIO")]
	if events[3]["outcome"] != want {
		t.Fatal("incorrect outbound outcome")
	}
	b, _ := json.Marshal(map[string]any{"ids": []string{ids[0], ids[3]}, "events": events})
	if os.WriteFile(os.Getenv("CORRELATION_RESULT"), b, 0600) != nil {
		t.Fatal("write result")
	}
}
