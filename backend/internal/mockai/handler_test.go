package mockai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func invoke(t *testing.T, mode bool, scenario, body string) *httptest.ResponseRecorder {
	t.Helper()
	h, err := New(mode, scenario)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Authorization", ProviderCredential)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestContract(t *testing.T) {
	for input, expected := range map[string]string{"breakfast for two": "oats, milk, and bananas", "vegetarian dinner": "rice, beans, and broccoli", "snacks for a picnic": "apples, crackers, and cheese", "unknown": "What meal", "server-error": "What meal"} {
		body, _ := json.Marshal(Request{Model: Model, Messages: []Message{{Role: "user", Content: input}}})
		a := invoke(t, false, "", string(body))
		b := invoke(t, false, "", string(body))
		if a.Code != 200 || a.Body.String() != b.Body.String() || a.Body.Len() > MaxResponseBytes {
			t.Fatal("non-deterministic or unbounded response")
		}
		var res struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Model   string `json:"model"`
			Choices []struct {
				Index   int     `json:"index"`
				Message Message `json:"message"`
				Finish  string  `json:"finish_reason"`
			} `json:"choices"`
			Usage any `json:"usage"`
		}
		if err := json.Unmarshal(a.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if res.ID == "" || res.Object != "chat.completion" || res.Model != Model || len(res.Choices) != 1 || res.Choices[0].Message.Role != "assistant" || !strings.HasPrefix(res.Choices[0].Message.Content, "SIMULATED:") || res.Choices[0].Finish != "stop" || res.Usage != nil {
			t.Fatal("invalid contract")
		}
		if !strings.Contains(res.Choices[0].Message.Content, expected) {
			t.Fatal("unexpected deterministic answer; unknown text must clarify, never trigger faults")
		}
	}
}
func TestValidation(t *testing.T) {
	for _, body := range []string{`{`, `{} `, `{"model":"other","messages":[{"role":"user","content":"x"}]}`, `{"model":"cartside-mock-v1","stream":true,"messages":[{"role":"user","content":"x"}]}`, `{"model":"cartside-mock-v1","messages":[{"role":"tool","content":"x"}]}`, `{"model":"cartside-mock-v1","messages":[{"role":"user","content":" "}]}`, `{"model":"cartside-mock-v1","messages":[{"role":"user","content":"x"}],"endpoint":"http://evil"}`, strings.Repeat("x", MaxRequestBytes+1), `{} {}`} {
		if w := invoke(t, false, "", body); w.Code != 400 {
			t.Fatalf("expected rejection: %d", w.Code)
		}
	}
	h, _ := New(false, "")
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("missing provider auth accepted")
	}
	r.Header.Set("Authorization", ProviderCredential)
	r.Header.Set("X-Cartside-AI-Key", "must-not-arrive")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("service credential reached provider")
	}
}
func TestFaultsAndHealth(t *testing.T) {
	const body = `{"model":"cartside-mock-v1","messages":[{"role":"user","content":"breakfast for two"}]}`
	for scenario, code := range map[string]int{"normal": 200, "malformed": 200, "rate-limit": 429, "server-error": 500, "delay": 200} {
		t.Run(scenario, func(t *testing.T) {
			h, err := New(true, scenario)
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			start := time.Now()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
			if w.Code != 200 || time.Since(start) > time.Second {
				t.Fatal("health affected by fault")
			}
			start = time.Now()
			w = invoke(t, true, scenario, body)
			if w.Code != code {
				t.Fatal(w.Code)
			}
			if scenario == "malformed" && json.Valid(w.Body.Bytes()) {
				t.Fatal("fault must be malformed")
			}
			if scenario == "delay" && time.Since(start) < 3*time.Second {
				t.Fatal("delay absent")
			}
			if scenario != "normal" {
				if _, err := New(false, scenario); err == nil {
					t.Fatal("fault enabled outside test mode")
				}
			}
		})
	}
	if _, err := New(true, "unknown"); err == nil {
		t.Fatal("unknown scenario accepted")
	}
	h, _ := New(false, "")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/test/stats", nil))
	if w.Code != 404 {
		t.Fatal("test endpoint exposed")
	}
}
