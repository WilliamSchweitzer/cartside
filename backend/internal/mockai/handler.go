// Package mockai implements a deliberately simulated, bounded Chat Completions provider.
package mockai

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const Model = "cartside-mock-v1"
const ProviderCredential = "Bearer cartside-local-fake-provider-only"
const MaxRequestBytes = 8192
const MaxResponseBytes = 2048

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Request struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// New's fault controls are process configuration, never interpreted from customer text.
// Test mode is required for both fault injection and the invocation counter endpoint.
func New(testMode bool, scenario string) (http.Handler, error) {
	if scenario == "" {
		scenario = "normal"
	}
	if (!testMode && scenario != "normal") || !map[string]bool{"normal": true, "malformed": true, "rate-limit": true, "server-error": true, "delay": true}[scenario] {
		return nil, errors.New("invalid local mock scenario configuration")
	}
	var calls atomic.Uint64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	if testMode {
		mux.HandleFunc("GET /test/stats", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]uint64{"calls": calls.Load()}) })
	}
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// The service credential and browser identity must never reach this boundary.
		if r.Header.Get("Authorization") != ProviderCredential || r.Header.Get("X-Cartside-AI-Key") != "" || r.Header.Get("Cookie") != "" {
			http.Error(w, "invalid provider credentials", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var req Request
		if err := dec.Decode(&req); err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			http.Error(w, "invalid request", 400)
			return
		}
		if req.Model != Model || req.Stream || len(req.Messages) == 0 || len(req.Messages) > 16 {
			http.Error(w, "unsupported request", 400)
			return
		}
		last := ""
		for _, m := range req.Messages {
			if (m.Role != "user" && m.Role != "system" && m.Role != "assistant") || strings.TrimSpace(m.Content) == "" {
				http.Error(w, "invalid message", 400)
				return
			}
			if m.Role == "user" {
				last = strings.ToLower(strings.TrimSpace(m.Content))
			}
		}
		switch scenario {
		case "malformed":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"choices":`))
			return
		case "rate-limit":
			w.Header().Set("Retry-After", "1")
			http.Error(w, "simulated rate limit", 429)
			return
		case "server-error":
			http.Error(w, "simulated server failure", 500)
			return
		case "delay":
			select {
			case <-time.After(3 * time.Second):
			case <-r.Context().Done():
				return
			}
		}
		content := "SIMULATED: What meal, dietary needs, and number of people are you shopping for?"
		switch last {
		case "breakfast for two":
			content = "SIMULATED: For breakfast for two, consider oats, milk, and bananas. This is a mock response, not catalog-backed advice."
		case "vegetarian dinner":
			content = "SIMULATED: For a vegetarian dinner, consider rice, beans, and broccoli. This is a mock response, not catalog-backed advice."
		case "snacks for a picnic":
			content = "SIMULATED: For picnic snacks, consider apples, crackers, and cheese. This is a mock response, not catalog-backed advice."
		}
		// Usage is intentionally omitted: the pinned plugin accepts its absence.
		writeJSON(w, map[string]any{"id": "chatcmpl-cartside-simulated", "object": "chat.completion", "created": 0, "model": Model, "choices": []any{map[string]any{"index": 0, "message": Message{Role: "assistant", Content: content}, "finish_reason": "stop"}}})
	})
	return mux, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	body, err := json.Marshal(v)
	if err != nil || len(body) > MaxResponseBytes {
		http.Error(w, "mock response limit", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(body)
}
