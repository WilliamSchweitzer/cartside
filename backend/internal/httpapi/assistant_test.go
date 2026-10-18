package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/auth"
	"github.com/WilliamSchweitzer/cartside/backend/internal/shoppingassistant"
)

type assistantFunc func(context.Context, string) (shoppingassistant.Result, error)

func (f assistantFunc) Suggest(c context.Context, s string) (shoppingassistant.Result, error) {
	return f(c, s)
}
func TestAssistantParsing(t *testing.T) {
	ctx := checkoutContext(t, "https://issuer.example", "alice")
	calls := 0
	service := assistantFunc(func(context.Context, string) (shoppingassistant.Result, error) {
		calls++
		return shoppingassistant.Result{Version: 1}, nil
	})
	l, _ := NewAssistantLimiter(100, 100, time.Minute, time.Now)
	handler := assistantHandler(service, l)
	for _, body := range []string{`{"message":"\ud800"}`, `{"message":"\udc00"}`, `{}`, `null`, `[]`, `{"Message":"x"}`, `{"message":null}`, `{"message":""}`, `{"message":" \t\n"}`, `{"message":1}`, `{"message":"x","message":"y"}`, `{"message":"x","role":"user"}`, `{"message":"x"} {}`, `{"message":"` + strings.Repeat("é", 129) + `"}`, `{"message":"` + strings.Repeat("x", 2049) + `"}`, "{\"message\":\"\xff\"}"} {
		r := httptest.NewRequest("POST", "/assistant/messages", strings.NewReader(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != 400 && w.Code != 413 {
			t.Fatalf("accepted invalid body: %d", w.Code)
		}
	}
	if calls != 0 || len(l.entries) != 0 {
		t.Fatal("invalid request consumed admission")
	}
	for _, body := range []string{`{"message":"breakfast for two"}`, `{"message":"` + strings.Repeat("é", 128) + `"}`, `{"message":"\u00e9"}`, `{"message":"\ud83d\ude00"}`} {
		r := httptest.NewRequest("POST", "/assistant/messages", strings.NewReader(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json; charset=utf-8")
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"suggestions":[]`) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("bad success %d %s", w.Code, w.Body)
		}
	}
	r := httptest.NewRequest("POST", "/assistant/messages", strings.NewReader(`{"message":"x"}`)).WithContext(ctx)
	w := httptest.NewRecorder()
	handler(w, r)
	if w.Code != 400 || calls != 4 {
		t.Fatal("content type")
	}
}
func TestAssistantBoundary(t *testing.T) {
	_, middleware, token := checkoutIdentity(t, "https://issuer.example", "alice")
	csrf, _ := NewCSRF("http://localhost:9080")
	l, _ := NewAssistantLimiter(1, 10, time.Minute, time.Now)
	calls := 0
	service := assistantFunc(func(context.Context, string) (shoppingassistant.Result, error) {
		calls++
		return shoppingassistant.Result{}, nil
	})
	for _, tc := range []struct {
		token, csrf, enabled bool
		status               int
		code                 string
	}{
		{false, true, true, 401, "unauthorized"}, {true, false, true, 403, "csrf_required"}, {true, true, false, 503, "assistant_unavailable"}, {true, true, true, 200, ""}, {true, true, true, 429, "assistant_rate_limited"},
	} {
		var a Assistant
		if tc.enabled {
			a = service
		}
		router := NewRouterWithAssistant(nil, middleware, nil, csrf, nil, nil, a, l)
		r := httptest.NewRequest("POST", "/assistant/messages", strings.NewReader(`{"message":"x"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-User-ID", "forged")
		if tc.token {
			r.Header.Set(auth.TokenHeader, token)
		}
		if tc.csrf {
			r.Header.Set("Origin", "http://localhost:9080")
			r.Header.Set("X-CSRF-Protection", "1")
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body)
		}
		if tc.status == 429 && w.Header().Get("Retry-After") == "" {
			t.Fatal("missing retry")
		}
	}
	if calls != 1 {
		t.Fatal("blocked request invoked assistant")
	}
}
func TestAssistantErrors(t *testing.T) {
	ctx := checkoutContext(t, "https://issuer.example", "alice")
	for _, tc := range []struct {
		code   shoppingassistant.ErrorCode
		status int
		want   string
	}{
		{shoppingassistant.InvalidInput, 400, "invalid_input"}, {shoppingassistant.RateLimited, 429, "provider_rate_limited"}, {shoppingassistant.Timeout, 504, "timeout"}, {shoppingassistant.Unavailable, 502, "provider_unavailable"}, {shoppingassistant.InvalidOutput, 502, "invalid_provider_output"}, {shoppingassistant.TooLarge, 502, "invalid_provider_output"}, {shoppingassistant.CatalogFailure, 503, "catalog_unavailable"}, {shoppingassistant.CatalogChanged, 409, "catalog_changed"},
	} {
		w := httptest.NewRecorder()
		l, _ := NewAssistantLimiter(1, 1, time.Minute, time.Now)
		service := assistantFunc(func(context.Context, string) (shoppingassistant.Result, error) {
			return shoppingassistant.Result{}, &shoppingassistant.Error{Code: tc.code}
		})
		r := httptest.NewRequest("POST", "/assistant/messages", strings.NewReader(`{"message":"x"}`)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		assistantHandler(service, l)(w, r)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.want) {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body)
		}
	}
	w := httptest.NewRecorder()
	assistantServiceError(w, errors.New("secret upstream body"))
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatal("leaked error")
	}
}
func TestAssistantLimiter(t *testing.T) {
	now := time.Unix(100, 0)
	l, _ := NewAssistantLimiter(2, 2, time.Minute, func() time.Time { return now })
	a := auth.Principal{Issuer: "a", Subject: "same"}
	b := auth.Principal{Issuer: "b", Subject: "same"}
	c := auth.Principal{Issuer: "a", Subject: "other"}
	release, _, _ := l.admit(a)
	if release == nil {
		t.Fatal("admission")
	}
	if f, code, retry := l.admit(a); f != nil || code != "assistant_busy" || retry < 1 {
		t.Fatal("concurrency")
	}
	release()
	release, _, _ = l.admit(a)
	release()
	if f, code, retry := l.admit(a); f != nil || code != "assistant_rate_limited" || retry != 60 {
		t.Fatal("rate")
	}
	releaseB, _, _ := l.admit(b)
	if releaseB == nil {
		t.Fatal("issuer isolation")
	}
	if f, code, _ := l.admit(c); f != nil || code != "assistant_capacity" {
		t.Fatal("capacity")
	}
	now = now.Add(time.Minute)
	// Expired busy b survives cleanup, idle a expires; c can enter.
	releaseC, _, _ := l.admit(c)
	if releaseC == nil || len(l.entries) != 2 || l.entries[b] == nil {
		t.Fatal("cleanup evicted busy user")
	}
	releaseC()
	releaseB()
	now = now.Add(time.Minute)
	release, _, _ = l.admit(a)
	if release == nil || len(l.entries) != 1 {
		t.Fatal("inactive cleanup")
	}
	release()
}
func TestAssistantConcurrentAdmissions(t *testing.T) {
	l, _ := NewAssistantLimiter(100, 1, time.Minute, time.Now)
	p := auth.Principal{Issuer: "a", Subject: "a"}
	var successes atomic.Int32
	var wg sync.WaitGroup
	releases := make(chan func(), 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if f, _, _ := l.admit(p); f != nil {
				successes.Add(1)
				releases <- f
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("multiple concurrent admissions")
	}
	(<-releases)()
}
func TestAssistantSlotRelease(t *testing.T) {
	ctx := checkoutContext(t, "https://issuer.example", "alice")
	for _, cancel := range []bool{false, true} {
		l, _ := NewAssistantLimiter(2, 1, time.Minute, time.Now)
		entered := make(chan struct{})
		done := make(chan struct{})
		service := assistantFunc(func(c context.Context, _ string) (shoppingassistant.Result, error) {
			close(entered)
			if cancel {
				<-c.Done()
			}
			return shoppingassistant.Result{}, &shoppingassistant.Error{Code: shoppingassistant.Unavailable}
		})
		c, stop := context.WithCancel(ctx)
		r := httptest.NewRequest("POST", "/assistant/messages", strings.NewReader(`{"message":"x"}`)).WithContext(c)
		r.Header.Set("Content-Type", "application/json")
		go func() { assistantHandler(service, l)(httptest.NewRecorder(), r); close(done) }()
		<-entered
		stop()
		<-done
		p, _ := auth.FromContext(ctx)
		f, _, _ := l.admit(p)
		if f == nil {
			t.Fatal("slot not released")
		}
		f()
		if f, code, _ := l.admit(p); f != nil || code != "assistant_rate_limited" {
			t.Fatal("failure/cancellation refunded admission")
		}
	}
}
func TestAssistantConfiguration(t *testing.T) {
	for _, tc := range []struct {
		env            map[string]string
		valid, enabled bool
	}{
		{map[string]string{}, true, false}, {map[string]string{"CARTSIDE_ASSISTANT_ENABLED": "false", "CARTSIDE_AI_GATEWAY_URL": "bad"}, true, false},
		{map[string]string{"CARTSIDE_ASSISTANT_ENABLED": "yes"}, false, false}, {map[string]string{"CARTSIDE_ASSISTANT_ENABLED": "true"}, false, false},
		{map[string]string{"CARTSIDE_ASSISTANT_ENABLED": "true", "CARTSIDE_AI_GATEWAY_URL": "http://localhost:9080/internal/ai/chat/completions", "CARTSIDE_AI_SERVICE_KEY": strings.Repeat("a", 32)}, true, true},
		{map[string]string{"CARTSIDE_ASSISTANT_ENABLED": "true", "CARTSIDE_ASSISTANT_MAX_USERS": "0"}, false, false},
	} {
		a, l, e := ConfigureAssistant(func(k string) string { return tc.env[k] }, nil)
		if (e == nil) != tc.valid || (a != nil) != tc.enabled || (l != nil) != tc.enabled {
			t.Fatalf("configuration: %v", e)
		}
	}
}

func TestAssistantBlockedAdmissionsDoNotInvoke(t *testing.T) {
	ctx := checkoutContext(t, "https://issuer.example", "alice")
	other := checkoutContext(t, "https://issuer.example", "bob")
	l, _ := NewAssistantLimiter(1, 1, time.Minute, time.Now)
	entered, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	service := assistantFunc(func(context.Context, string) (shoppingassistant.Result, error) {
		calls.Add(1)
		close(entered)
		<-finish
		return shoppingassistant.Result{}, nil
	})
	handler := assistantHandler(service, l)
	request := func(ctx context.Context) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/assistant/messages", strings.NewReader(`{"message":"breakfast for two"}`)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		// Forged owner headers cannot change the verified identity used for limits.
		r.Header.Set("X-User-ID", time.Now().String())
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	done := make(chan struct{})
	go func() { defer close(done); request(ctx) }()
	<-entered
	if w := request(ctx); w.Code != 429 || !strings.Contains(w.Body.String(), "assistant_busy") {
		t.Fatal("concurrent handler admitted")
	}
	if w := request(other); w.Code != 429 || !strings.Contains(w.Body.String(), "assistant_capacity") {
		t.Fatal("capacity handler admitted")
	}
	close(finish)
	<-done
	if w := request(ctx); w.Code != 429 || !strings.Contains(w.Body.String(), "assistant_rate_limited") {
		t.Fatal("rate handler admitted")
	}
	if calls.Load() != 1 {
		t.Fatal("blocked handler invoked service")
	}
}

func TestAssistantDisabledDoesNotReadCredentials(t *testing.T) {
	a, l, e := ConfigureAssistant(func(k string) string {
		if k != "CARTSIDE_ASSISTANT_ENABLED" {
			t.Fatalf("disabled configuration read %s", k)
		}
		return "false"
	}, nil)
	if a != nil || l != nil || e != nil {
		t.Fatal("disabled configuration")
	}
}
