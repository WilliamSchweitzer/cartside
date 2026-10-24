package observability

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestCorrelationAndRedaction(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	r := chi.NewRouter()
	r.Use(Middleware("api", ChiRoute))
	r.Get("/products/{id}", func(w http.ResponseWriter, r *http.Request) {
		if ID(r.Context()) != w.Header().Get(Header) {
			t.Error("context mismatch")
		}
		w.Write([]byte("sensitive-output"))
	})
	r.Get("/empty", func(http.ResponseWriter, *http.Request) {})
	r.Get("/failure", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	for _, id := range []string{"", "bad.secret", strings.Repeat("a", 65), "safe-ID_123"} {
		req := httptest.NewRequest("GET", "/products/sensitive-id?q=private-search", strings.NewReader("private-prompt"))
		req.Header.Set(Header, id)
		req.Header.Set("Authorization", "Bearer secret-token")
		req.Header.Set("Cookie", "session=secret-cookie")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		got := w.Header().Get(Header)
		if !ValidID(got) || (id == "safe-ID_123" && got != id) || (id != "safe-ID_123" && got == id) {
			t.Fatal("invalid correlation")
		}
	}
	for _, path := range []string{"/empty", "/failure", "/private-unmatched?code=secret-code"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
	}
	for _, secret := range []string{"sensitive-id", "private-search", "private-prompt", "secret-token", "secret-cookie", "sensitive-output", "private-unmatched", "secret-code"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("log leaked %s", secret)
		}
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 7 {
		t.Fatal("completion count", len(lines))
	}
	for i, line := range lines {
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) != nil {
			t.Fatal("invalid JSON")
		}
		if i < 4 && event["route"] != "/products/{id}" {
			t.Fatal(event)
		}
		if i == 4 && event["status"] != float64(200) {
			t.Fatal(event)
		}
		if i == 5 && event["status"] != float64(503) {
			t.Fatal(event)
		}
		if i == 6 && event["route"] != "unmatched" {
			t.Fatal(event)
		}
	}
}
func TestConcurrentIsolation(t *testing.T) {
	h := Middleware("api", func(*http.Request) string { return "/test" })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ID(r.Context()) != r.Header.Get(Header) {
			t.Error("cross-request ID")
		}
	}))
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/", nil)
			id := fmt.Sprintf("request-%d", i)
			req.Header.Set(Header, id)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Header().Get(Header) != id {
				t.Error("cross-response ID")
			}
		}(i)
	}
	wg.Wait()
}

func TestDuplicateIDsAndFlushing(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	h := Middleware("api", func(*http.Request) string { return "/stream" })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("lost Flusher")
		}
		flusher.Flush()
		if ID(r.Context()) == "one" || ID(r.Context()) == "two" {
			t.Fatal("accepted duplicate IDs")
		}
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Add(Header, "one")
	req.Header.Add(Header, "two")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if !w.Flushed || !ValidID(w.Header().Get(Header)) {
		t.Fatal("flush or fallback failed")
	}
	var event map[string]any
	json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &event)
	if event["status"] != float64(200) {
		t.Fatal(event)
	}
}

func TestInterruptedRequestDoesNotLogPanicValue(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	h := Middleware("api", func(*http.Request) string { return "/test" })(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("secret-panic") }))
	func() {
		defer func() {
			if recover() == nil {
				t.Error("middleware swallowed panic")
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	}()
	var event map[string]any
	if json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &event) != nil || event["error_category"] != "interrupted" || event["status"] != float64(0) || strings.Contains(logs.String(), "secret-panic") {
		t.Fatal("invalid interrupted event")
	}
}
