package mockai

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSafeCorrelatedCompletion(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	h, err := New(true, "rate-limit")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/chat/completions?token=private-query", strings.NewReader(`{"model":"cartside-mock-v1","messages":[{"role":"user","content":"private-prompt"}],"stream":false}`))
	r.Header.Set("Authorization", ProviderCredential)
	r.Header.Set("X-Request-ID", "mock-correlation")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var e map[string]any
	if json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &e) != nil {
		t.Fatal("expected one JSON event")
	}
	if e["request_id"] != "mock-correlation" || e["route"] != "POST /v1/chat/completions" || e["status"] != float64(429) || w.Code != 429 {
		t.Fatal(e)
	}
	for _, s := range []string{"private-query", "private-prompt", ProviderCredential, "simulated rate limit"} {
		if strings.Contains(logs.String(), s) {
			t.Fatal("sensitive log")
		}
	}
}
