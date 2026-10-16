package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/auth"
)

func TestReadiness(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
		body string
	}{
		{"available", nil, 200, `{"status":"ok"}`},
		{"unavailable", errors.New("secret database details"), 503, `{"status":"unavailable"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			router := NewRouter(func(ctx context.Context) error {
				called = true
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > time.Second {
					t.Error("missing short readiness deadline")
				}
				return tc.err
			}, testVerifier(t).Middleware, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("GET", "/health/ready", nil))
			if !called || w.Code != tc.code || w.Body.String() != tc.body || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("unexpected readiness response: %d %s", w.Code, w.Body.String())
			}
			w = httptest.NewRecorder()
			called = false
			router.ServeHTTP(w, httptest.NewRequest("GET", "/health/live", nil))
			if called || w.Code != 200 || w.Body.String() != `{"status":"ok"}` {
				t.Fatal("liveness must succeed without checking dependencies")
			}
		})
	}
}

func TestReadinessRecovery(t *testing.T) {
	available := false
	router := NewRouter(func(context.Context) error {
		if !available {
			return errors.New("offline")
		}
		return nil
	}, testVerifier(t).Middleware, nil)
	for _, code := range []int{503, 200} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/health/ready", nil))
		if w.Code != code {
			t.Fatalf("got %d, want %d", w.Code, code)
		}
		available = true
	}
}

func testVerifier(t *testing.T) *auth.Verifier {
	t.Helper()
	v, err := auth.New("https://issuer.example", "cartside-api", "https://issuer.example/certs")
	if err != nil {
		t.Fatal(err)
	}
	return v
}
