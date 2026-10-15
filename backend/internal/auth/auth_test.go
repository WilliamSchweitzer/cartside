package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/auth"
	"github.com/WilliamSchweitzer/cartside/backend/internal/httpapi"
	jose "github.com/go-jose/go-jose/v4"
)

const issuer = "https://identity.example/realms/cartside"

func key(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	return k
}
func signed(t *testing.T, k *rsa.PrivateKey, kid string, alg jose.SignatureAlgorithm, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: k}, (&jose.SignerOptions{}).WithHeader("kid", kid))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := signer.Sign(b)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := obj.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func claims() map[string]any {
	return map[string]any{"iss": issuer, "aud": []string{"account", "cartside-api"}, "sub": "customer-123", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Add(-time.Minute).Unix()}
}

func TestAuthentication(t *testing.T) {
	k := key(t)
	other := key(t)
	var mu sync.Mutex
	current, kid, requests, offline := k, "first", 0, false
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		if offline {
			http.Error(w, "offline", 503)
			return
		}
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &current.PublicKey, KeyID: kid, Algorithm: "RS256", Use: "sig"}}})
	}))
	defer jwks.Close()
	v, err := auth.New(issuer, "cartside-api", jwks.URL)
	if err != nil {
		t.Fatal(err)
	}
	router := httpapi.NewRouter(func(context.Context) error { return nil }, v.Middleware)
	cases := []struct {
		name       string
		change     func(map[string]any)
		signingKey *rsa.PrivateKey
		alg        jose.SignatureAlgorithm
		raw        string
		code       int
	}{
		{name: "missing", raw: "", code: 401},
		{name: "malformed", raw: "not.a.jwt", code: 401},
		{name: "unsigned", raw: "eyJhbGciOiJub25lIn0.eyJzdWIiOiJmb3JnZWQifQ.", code: 401},
		{name: "invalid signature", signingKey: other, code: 401},
		{name: "disallowed algorithm", alg: jose.RS512, code: 401},
		{name: "wrong issuer", change: func(c map[string]any) { c["iss"] = "https://wrong.example" }, code: 401},
		{name: "missing issuer", change: func(c map[string]any) { delete(c, "iss") }, code: 401},
		{name: "wrong audience", change: func(c map[string]any) { c["aud"] = "cartside-apisix" }, code: 401},
		{name: "missing audience", change: func(c map[string]any) { delete(c, "aud") }, code: 401},
		{name: "expired", change: func(c map[string]any) { c["exp"] = time.Now().Add(-time.Second).Unix() }, code: 401},
		{name: "missing expiry", change: func(c map[string]any) { delete(c, "exp") }, code: 401},
		{name: "missing subject", change: func(c map[string]any) { delete(c, "sub") }, code: 401},
		{name: "empty subject", change: func(c map[string]any) { c["sub"] = " " }, code: 401},
		{name: "future nbf", change: func(c map[string]any) { c["nbf"] = time.Now().Add(time.Minute).Unix() }, code: 401},
		{name: "future iat", change: func(c map[string]any) { c["iat"] = time.Now().Add(time.Minute).Unix() }, code: 401},
		{name: "malformed time", change: func(c map[string]any) { c["nbf"] = "tomorrow" }, code: 401},
		{name: "valid", code: 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.raw
			if tc.raw == "" && tc.name != "missing" {
				c := claims()
				if tc.change != nil {
					tc.change(c)
				}
				sk := tc.signingKey
				if sk == nil {
					sk = k
				}
				alg := tc.alg
				if alg == "" {
					alg = jose.RS256
				}
				raw = signed(t, sk, "first", alg, c)
			}
			req := httptest.NewRequest("GET", "/me", nil)
			if raw != "" {
				req.Header.Set(auth.TokenHeader, raw)
			}
			req.Header.Set("X-User-ID", "forged")
			req.Header.Set("X-Forwarded-User", "forged")
			req.Header.Set("Authorization", "Bearer fake")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.code || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("unexpected status/cache: %d %v", w.Code, w.Header())
			}
			if tc.code == 401 && w.Body.String() != `{"error":"unauthorized"}` {
				t.Fatalf("non-generic failure: %s", w.Body.String())
			}
			if tc.code == 200 {
				var body map[string]string
				if json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body) != 2 || body["subject"] != "customer-123" || body["issuer"] != issuer {
					t.Fatalf("unexpected principal: %s", w.Body.String())
				}
			}
		})
	}
	valid := signed(t, k, "first", jose.RS256, claims())
	check := func(raw, path string, want int) {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		if raw != "" {
			req.Header.Set(auth.TokenHeader, raw)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s: got %d want %d", path, w.Code, want)
		}
	}
	mu.Lock()
	before := requests
	offline = true
	mu.Unlock()
	check(valid, "/me", 200) // Cached keys continue to work during a provider outage.
	mu.Lock()
	if requests != before {
		t.Error("cached key fetched again")
	}
	offline = false
	current = other
	kid = "rotated"
	mu.Unlock()
	rotated := signed(t, other, "rotated", jose.RS256, claims())
	check(rotated, "/me", 200)
	mu.Lock()
	offline = true
	mu.Unlock()
	check(signed(t, k, "unknown", jose.RS256, claims()), "/me", 401)
	check("", "/health/live", 200)
	check("", "/health/ready", 200)
	check("", "/nonexistent", 401)
	check(rotated, "/nonexistent", 404)
	for _, method := range []string{"POST", "OPTIONS", "HEAD"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, "/me", nil))
		if w.Code != 401 {
			t.Fatalf("unprotected method %s", method)
		}
	}
	req := httptest.NewRequest("GET", "/me", nil)
	req.Header[auth.TokenHeader] = []string{valid, valid}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("duplicate tokens accepted")
	}
}

func TestConfiguration(t *testing.T) {
	for _, c := range [][3]string{{"", "api", "https://keys.example"}, {issuer, "", "https://keys.example"}, {issuer, "api", ""}, {"not-a-url", "api", "https://keys.example"}, {issuer, "api", "ftp://keys.example"}, {issuer, "api", "https://user:secret@keys.example"}} {
		if _, err := auth.New(c[0], c[1], c[2]); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	// No discovery or key fetch at construction: health startup is independent.
	if _, err := auth.New(issuer, "cartside-api", "http://127.0.0.1:1/certs"); err != nil {
		t.Fatal(err)
	}
}
