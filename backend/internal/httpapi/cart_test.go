package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/auth"
	"github.com/WilliamSchweitzer/cartside/backend/internal/cart"
	"github.com/WilliamSchweitzer/cartside/backend/internal/catalog"
	jose "github.com/go-jose/go-jose/v4"
)

type cartStub struct {
	reads, writes int
	err           error
	owner         auth.Principal
}

func (s *cartStub) Get(ctx context.Context) (cart.Cart, error) {
	s.reads++
	s.owner, _ = auth.FromContext(ctx)
	return cart.Cart{Currency: "USD"}, s.err
}
func (s *cartStub) Add(ctx context.Context, _ string, _ int) error {
	s.writes++
	s.owner, _ = auth.FromContext(ctx)
	return s.err
}
func (s *cartStub) SetQuantity(ctx context.Context, id string, q int) error { return s.Add(ctx, id, q) }
func (s *cartStub) Remove(ctx context.Context, id string) error             { return s.Add(ctx, id, 0) }

func TestCartHTTP(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
	}))
	defer jwks.Close()
	verifier, err := auth.New("https://issuer.example", "cartside-api", jwks.URL)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "test"))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"iss": "https://issuer.example", "aud": "cartside-api", "sub": "customer", "exp": time.Now().Add(time.Hour).Unix()})
	signed, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	csrf, err := NewCSRF("http://localhost:9080")
	if err != nil {
		t.Fatal(err)
	}
	const id = "bb5ce88f-908c-5a99-8ae7-90b60b00ae1b"
	valid := `{"product_id":"` + id + `","quantity":1}`

	for _, tc := range []struct {
		name                 string
		origin, custom, site []string
		status               int
	}{
		{"match", []string{"http://localhost:9080"}, []string{"1"}, []string{"same-origin"}, 204},
		{"no metadata", []string{"http://localhost:9080"}, []string{"1"}, nil, 204},
		{"host case", []string{"http://LOCALHOST:9080"}, []string{"1"}, nil, 204},
		{"scheme", []string{"https://localhost:9080"}, []string{"1"}, nil, 403},
		{"host", []string{"http://evil.example:9080"}, []string{"1"}, nil, 403},
		{"port", []string{"http://localhost:9081"}, []string{"1"}, nil, 403},
		{"default port differs", []string{"http://localhost"}, []string{"1"}, nil, 403},
		{"missing origin", nil, []string{"1"}, nil, 403},
		{"null origin", []string{"null"}, []string{"1"}, nil, 403},
		{"malformed origin", []string{"://"}, []string{"1"}, nil, 403},
		{"origin path", []string{"http://localhost:9080/path"}, []string{"1"}, nil, 403},
		{"origin slash", []string{"http://localhost:9080/"}, []string{"1"}, nil, 403},
		{"origin credentials", []string{"http://user@localhost:9080"}, []string{"1"}, nil, 403},
		{"origin query", []string{"http://localhost:9080?"}, []string{"1"}, nil, 403},
		{"origin fragment", []string{"http://localhost:9080#"}, []string{"1"}, nil, 403},
		{"repeated origin", []string{"http://localhost:9080", "http://localhost:9080"}, []string{"1"}, nil, 403},
		{"origin list", []string{"http://localhost:9080 http://evil.example"}, []string{"1"}, nil, 403},
		{"origin comma", []string{"http://localhost:9080,http://evil.example"}, []string{"1"}, nil, 403},
		{"missing custom", []string{"http://localhost:9080"}, nil, nil, 403},
		{"repeated custom", []string{"http://localhost:9080"}, []string{"1", "1"}, nil, 403},
		{"custom list", []string{"http://localhost:9080"}, []string{"1, 1"}, nil, 403},
		{"wrong custom", []string{"http://localhost:9080"}, []string{"0"}, nil, 403},
		{"same site", []string{"http://localhost:9080"}, []string{"1"}, []string{"same-site"}, 403},
		{"cross site", []string{"http://localhost:9080"}, []string{"1"}, []string{"cross-site"}, 403},
		{"none", []string{"http://localhost:9080"}, []string{"1"}, []string{"none"}, 403},
		{"empty metadata", []string{"http://localhost:9080"}, []string{"1"}, []string{""}, 403},
		{"malformed metadata", []string{"http://localhost:9080"}, []string{"1"}, []string{"Same-Origin"}, 403},
		{"metadata list", []string{"http://localhost:9080"}, []string{"1"}, []string{"same-origin, same-origin"}, 403},
		{"repeated metadata", []string{"http://localhost:9080"}, []string{"1"}, []string{"same-origin", "same-origin"}, 403},
	} {
		for _, method := range []string{"POST", "PUT", "DELETE"} {
			t.Run(tc.name+"/"+method, func(t *testing.T) {
				service := &cartStub{}
				router := NewRouter(nil, verifier.Middleware, nil, csrf, service)
				path, body := "/cart/items/"+id, `{"quantity":1}`
				if method == "POST" {
					path, body = "/cart/items", valid
				}
				req := httptest.NewRequest(method, path, strings.NewReader(body))
				req.Host = "evil.example"
				req.Header.Set("Forwarded", "host=localhost:9080;proto=http")
				req.Header.Set("X-Forwarded-Host", "localhost:9080")
				req.Header.Set("X-Forwarded-Proto", "http")
				req.Header.Set(auth.TokenHeader, token)
				req.Header.Set("Content-Type", "application/json")
				for key, values := range map[string][]string{"Origin": tc.origin, "X-CSRF-Protection": tc.custom, "Sec-Fetch-Site": tc.site} {
					for _, value := range values {
						req.Header.Add(key, value)
					}
				}
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				writes := 0
				if tc.status == 204 {
					writes = 1
				}
				if w.Code != tc.status || service.writes != writes {
					t.Fatalf("status=%d writes=%d body=%s", w.Code, service.writes, w.Body.String())
				}
				if tc.status == 403 && (w.Body.String() != "{\"error\":\"CSRF protection required\"}\n" || w.Header().Get("Content-Type") != "application/json") {
					t.Fatal("non-generic JSON rejection", w.Body.String())
				}
			})
		}
	}
	for _, tc := range []struct {
		name, method, path, body, csrf, site, content string
		anonymous                                     bool
		err                                           error
		status, writes                                int
	}{
		{name: "empty", method: "GET", path: "/cart", status: 200},
		{name: "add", method: "POST", path: "/cart/items", body: valid, csrf: "1", status: 204, writes: 1},
		{name: "set", method: "PUT", path: "/cart/items/" + id, body: `{"quantity":2}`, csrf: "1", status: 204, writes: 1},
		{name: "remove", method: "DELETE", path: "/cart/items/" + id, csrf: "1", status: 204, writes: 1},
		{name: "csrf", method: "POST", path: "/cart/items", body: valid, status: 403},
		{name: "cross site", method: "DELETE", path: "/cart/items/" + id, csrf: "1", site: "cross-site", status: 403},
		{name: "media", method: "POST", path: "/cart/items", body: valid, csrf: "1", content: "text/plain", status: 415},
		{name: "bad id", method: "DELETE", path: "/cart/items/nope", csrf: "1", status: 400},
		{name: "quantity", method: "POST", path: "/cart/items", body: `{"product_id":"` + id + `","quantity":100}`, csrf: "1", status: 400},
		{name: "missing", method: "POST", path: "/cart/items", body: `{}`, csrf: "1", status: 400},
		{name: "duplicate", method: "POST", path: "/cart/items", body: `{"product_id":"` + id + `","quantity":1,"quantity":2}`, csrf: "1", status: 400},
		{name: "unknown", method: "PUT", path: "/cart/items/" + id, body: `{"quantity":1,"owner":"attacker"}`, csrf: "1", status: 400},
		{name: "trailing", method: "POST", path: "/cart/items", body: valid + ` {}`, csrf: "1", status: 400},
		{name: "null", method: "PUT", path: "/cart/items/" + id, body: `{"quantity":null}`, csrf: "1", status: 400},
		{name: "large", method: "POST", path: "/cart/items", body: valid + strings.Repeat(" ", 4096), csrf: "1", status: 413},
		{name: "not found", method: "PUT", path: "/cart/items/" + id, body: `{"quantity":2}`, csrf: "1", err: cart.ErrItemNotFound, status: 404, writes: 1},
		{name: "unavailable", method: "POST", path: "/cart/items", body: valid, csrf: "1", err: cart.ErrUnavailable, status: 409, writes: 1},
		{name: "product missing", method: "POST", path: "/cart/items", body: valid, csrf: "1", err: catalog.ErrNotFound, status: 404, writes: 1},
		{name: "failure", method: "POST", path: "/cart/items", body: valid, csrf: "1", err: errors.New("secret"), status: 503, writes: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &cartStub{err: tc.err}
			router := NewRouter(nil, verifier.Middleware, nil, csrf, service)
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set(auth.TokenHeader, token)
			req.Header.Set("X-User-ID", "attacker")
			req.Header.Set("X-CSRF-Protection", tc.csrf)
			req.Header.Set("Origin", "http://localhost:9080")
			if tc.site != "" {
				req.Header.Set("Sec-Fetch-Site", tc.site)
			}
			content := tc.content
			if content == "" {
				content = "application/json"
			}
			req.Header.Set("Content-Type", content)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.status || service.writes != tc.writes || strings.Contains(w.Body.String(), "secret") {
				t.Fatalf("status=%d writes=%d body=%s", w.Code, service.writes, w.Body.String())
			}
			if tc.status == 204 && (service.reads != 0 || w.Body.Len() != 0) {
				t.Fatal("mutation performed read or returned body")
			}
			if tc.status == 200 && w.Body.String() != `{"version":0,"items":[],"subtotal_cents":0,"currency":"USD"}`+"\n" {
				t.Fatal(w.Body.String())
			}
			if service.reads+service.writes > 0 && service.owner.Subject != "customer" {
				t.Fatal("wrong owner")
			}
			// Every endpoint must reject a request lacking the verified token.
			req.Header.Del(auth.TokenHeader)
			w = httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != 401 {
				t.Fatalf("unauthenticated status %d", w.Code)
			}
		})
	}
}
