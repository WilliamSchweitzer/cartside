package shoppingassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/aiprotocol"
	"github.com/WilliamSchweitzer/cartside/backend/internal/observability"
)

func TestOutboundCorrelationAndSafeOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		cancel  bool
		timeout bool
		want    string
	}{
		{name: "malformed", status: 200, body: "secret-model-output", want: string(InvalidOutput)},
		{name: "rate-limit", status: 429, body: "secret-provider-error", want: string(RateLimited)},
		{name: "rejected-400", status: 400, body: "secret-provider-error", want: "request_rejected"},
		{name: "rejected-401", status: 401, body: "secret-provider-error", want: "request_rejected"},
		{name: "rejected-403", status: 403, body: "secret-provider-error", want: "request_rejected"},
		{name: "rejected-404", status: 404, body: "secret-provider-error", want: "request_rejected"},
		{name: "rejected-413", status: 413, body: "secret-provider-error", want: "request_rejected"},
		{name: "upstream-timeout", status: 408, want: string(Timeout)},
		{name: "gateway-timeout", status: 504, want: string(Timeout)},
		{name: "failure", status: 500, body: "secret-provider-error", want: string(Unavailable)},
		{name: "timeout", timeout: true, want: string(Timeout)},
		{name: "cancel", cancel: true, want: "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			defer slog.SetDefault(old)
			c := clientFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(observability.Header) != "correlation-test" {
					t.Error("missing ID")
				}
				if tc.timeout {
					time.Sleep(60 * time.Millisecond)
					return
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			metrics := observability.NewMetrics()
			c.metrics = metrics
			ctx := observability.WithID(context.Background(), "correlation-test")
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if tc.timeout {
				c.http.Timeout = 20 * time.Millisecond
			}
			_, err := c.Complete(ctx, aiprotocol.Input{Version: 1, Scenario: "secret-prompt", Candidates: []aiprotocol.Candidate{}})
			if err == nil {
				t.Fatal("expected failure")
			}
			w := httptest.NewRecorder()
			metrics.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
			for _, name := range []string{"cartside_assistant_calls_total", "cartside_assistant_call_duration_seconds_count"} {
				want := name + `{operation="shopping_completion",outcome="` + tc.want + `",provider="mock"} 1`
				if !strings.Contains(w.Body.String(), want) {
					t.Errorf("missing %s", want)
				}
			}
			for _, secret := range []string{"secret-prompt", "correlation-test", "secret-provider-error"} {
				if strings.Contains(w.Body.String(), secret) {
					t.Error("sensitive metric")
				}
			}
			var event map[string]any
			if json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &event) != nil {
				t.Fatalf("expected one JSON event: %s", logs.String())
			}
			if event["request_id"] != "correlation-test" || event["outcome"] != tc.want {
				t.Fatal(event)
			}
			for _, secret := range []string{"secret-prompt", "secret-model-output", "secret-provider-error", strings.Repeat("s", 32), "http://"} {
				if strings.Contains(logs.String(), secret) {
					t.Fatal("sensitive log")
				}
			}
		})
	}
}
