package shoppingassistant

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/WilliamSchweitzer/cartside/backend/internal/aiprotocol"
	"github.com/WilliamSchweitzer/cartside/backend/internal/catalog"
	"github.com/WilliamSchweitzer/cartside/backend/internal/mockai"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testID = "12345678-1234-1234-1234-123456789012"

type fakeCatalog struct {
	empty, unavailable, missing bool
	gets                        int
}

func (f *fakeCatalog) List(_ context.Context, q catalog.Query) (catalog.Page, error) {
	if q.Limit != 2 || q.Search == "" {
		panic("unbounded search")
	}
	if f.empty {
		return catalog.Page{}, nil
	}
	return catalog.Page{Items: []catalog.Product{{ID: testID, Name: "Candidate", PackageLabel: "1 bag", Available: true, PriceCents: 100}}}, nil
}
func (f *fakeCatalog) Get(_ context.Context, id string) (catalog.Product, error) {
	f.gets++
	if f.missing {
		return catalog.Product{}, catalog.ErrNotFound
	}
	return catalog.Product{ID: id, Name: "Updated", PackageLabel: "2 bags", PriceCents: 999, Currency: "USD", Available: !f.unavailable}, nil
}
func clientFor(t *testing.T, h http.Handler) *Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c, e := NewClient(s.URL+"/internal/ai/chat/completions", strings.Repeat("s", 32))
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func mockGateway(t *testing.T) *Client {
	h, _ := mockai.New(false, "")
	return clientFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-User-ID") != "" {
			t.Error("identity forwarded")
		}
		r.Header.Del("X-Cartside-AI-Key")
		r.Header.Set("Authorization", mockai.ProviderCredential)
		r.URL.Path = "/v1/chat/completions"
		h.ServeHTTP(w, r)
	}))
}
func assertCode(t *testing.T, e error, code ErrorCode) {
	t.Helper()
	var safe *Error
	if !errors.As(e, &safe) || safe.Code != code {
		t.Fatalf("error %v want %s", e, code)
	}
}
func TestScenarios(t *testing.T) {
	for scenario := range demoTerms {
		t.Run(scenario, func(t *testing.T) {
			f := &fakeCatalog{}
			r, e := New(f, mockGateway(t)).Suggest(context.Background(), scenario)
			if e != nil || len(r.Suggestions) != 1 || f.gets != 1 {
				t.Fatalf("%+v %v", r, e)
			}
			s := r.Suggestions[0]
			if s.PriceCents != 999 || s.Name != "Updated" || s.Package != "2 bags" || s.Currency != "USD" || !s.Available || r.Provider != "simulated: cartside-mock-v1" {
				t.Fatal(r)
			}
		})
	}
}
func TestClarificationAndChanges(t *testing.T) {
	for _, tc := range []struct {
		name    string
		f       fakeCatalog
		code    ErrorCode
		request string
	}{{name: "clarify", request: "other"}, {name: "empty", f: fakeCatalog{empty: true}, request: "breakfast for two"}, {name: "unavailable", f: fakeCatalog{unavailable: true}, code: CatalogChanged, request: "breakfast for two"}, {name: "removed", f: fakeCatalog{missing: true}, code: CatalogChanged, request: "breakfast for two"}} {
		t.Run(tc.name, func(t *testing.T) {
			r, e := New(&tc.f, mockGateway(t)).Suggest(context.Background(), tc.request)
			if tc.code != "" {
				assertCode(t, e, tc.code)
			} else if e != nil || len(r.Suggestions) != 0 || r.Text == "" {
				t.Fatalf("%+v %v", r, e)
			}
		})
	}
}
func envelope(content string) string {
	b, _ := json.Marshal(map[string]any{"id": "mock", "object": "chat.completion", "created": 0, "model": aiprotocol.Model, "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
	return string(b)
}
func TestUntrustedOutputNoRetries(t *testing.T) {
	valid := `{"version":1,"text":"SIMULATED: demo","suggestions":[{"product_id":"` + testID + `","quantity":1,"reason":"Demo"}]}`
	invalid := []string{"{", valid + ` {}`, strings.Replace(valid, testID, "unknown", 1), strings.Replace(valid, `}]}`, `},{"product_id":"`+testID+`","quantity":1,"reason":"Demo"}]}`, 1), strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1), strings.Replace(valid, `"version":1`, `"version":1,"price":1`, 1)}
	for _, q := range []string{"0", "100", "1.5", `"1"`, "null"} {
		invalid = append(invalid, strings.Replace(valid, `"quantity":1`, `"quantity":`+q, 1))
	}
	bodies := []string{envelope(valid)}
	for _, s := range invalid {
		bodies = append(bodies, envelope(s))
	}
	bodies = append(bodies, envelope(valid)+` {}`, strings.Replace(envelope(valid), `"created":0`, `"created":0,"created":0`, 1), strings.Replace(envelope(valid), `"created":0`, `"created":0,"extra":1`, 1))
	for i, b := range bodies {
		calls := 0
		c := clientFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.Write([]byte(b)) }))
		_, e := c.Complete(context.Background(), aiprotocol.Input{Candidates: []aiprotocol.Candidate{{ProductID: testID}}})
		if i == 0 {
			if e != nil {
				t.Fatal(e)
			}
		} else {
			assertCode(t, e, InvalidOutput)
		}
		if calls != 1 {
			t.Fatal("retry detected")
		}
	}
	for status, code := range map[int]ErrorCode{429: RateLimited, 504: Timeout, 500: Unavailable, 302: Unavailable} {
		calls := 0
		c := clientFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(status) }))
		_, e := c.Complete(context.Background(), aiprotocol.Input{})
		assertCode(t, e, code)
		if calls != 1 {
			t.Fatal("retry")
		}
	}
}
func TestBounds(t *testing.T) {
	calls := 0
	c := clientFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.Write([]byte(strings.Repeat("x", 2049))) }))
	_, e := c.Complete(context.Background(), aiprotocol.Input{Scenario: strings.Repeat("x", 8192)})
	assertCode(t, e, TooLarge)
	if calls != 0 {
		t.Fatal("oversized request sent")
	}
	_, e = c.Complete(context.Background(), aiprotocol.Input{})
	assertCode(t, e, TooLarge)
	c = clientFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(50 * time.Millisecond) }))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, e = c.Complete(ctx, aiprotocol.Input{})
	assertCode(t, e, Timeout)
}

func TestLocalClarificationNeverCallsModel(t *testing.T) {
	for _, tc := range []struct {
		request string
		empty   bool
	}{{"unknown request", false}, {"breakfast for two", true}} {
		result, err := New(&fakeCatalog{empty: tc.empty}, nil).Suggest(context.Background(), tc.request)
		if err != nil || result.Text == "" || len(result.Suggestions) != 0 {
			t.Fatalf("%+v %v", result, err)
		}
	}
}
func TestClientConfiguration(t *testing.T) {
	for _, u := range []string{"", "http://user:pass@localhost/internal/ai/chat/completions", "http://localhost/wrong", "http://localhost/internal/ai/chat/completions?q=x", "http://localhost/internal/ai/chat/completions#secret"} {
		if _, err := NewClient(u, strings.Repeat("s", 32)); err == nil {
			t.Fatal("invalid URL accepted")
		}
	}
	if _, err := NewClient("http://localhost/internal/ai/chat/completions", "replace_with_independent_random_hex"); err == nil {
		t.Fatal("placeholder accepted")
	}
}

func TestSuggestionLimits(t *testing.T) {
	for _, out := range []aiprotocol.Output{
		{Version: 1, Text: strings.Repeat("x", 401), Suggestions: []aiprotocol.Suggestion{}},
		{Version: 1, Text: "demo", Suggestions: []aiprotocol.Suggestion{{ProductID: testID, Quantity: 1, Reason: strings.Repeat("x", 121)}}},
		{Version: 1, Text: "demo", Suggestions: make([]aiprotocol.Suggestion, 6)},
	} {
		b, _ := json.Marshal(out)
		c := clientFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(envelope(string(b)))) }))
		_, err := c.Complete(context.Background(), aiprotocol.Input{Candidates: []aiprotocol.Candidate{{ProductID: testID}}})
		assertCode(t, err, InvalidOutput)
	}
}
