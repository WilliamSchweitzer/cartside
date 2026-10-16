package orders

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"github.com/WilliamSchweitzer/cartside/backend/internal/auth"
	"github.com/go-jose/go-jose/v4"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Use the real verifier to populate the private auth context key. No production
// API for injecting an unverified principal is introduced for the tests.
func verifiedContext(t *testing.T, issuer, subject string) context.Context {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
	}))
	defer jwks.Close()
	verifier, err := auth.New(issuer, "cartside-api", jwks.URL)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "test"))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := json.Marshal(map[string]any{"iss": issuer, "sub": subject, "aud": "cartside-api", "exp": time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.Sign(claims)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(auth.TokenHeader, token)
	req.Header.Set("X-User-ID", "forged-owner")
	var ctx context.Context
	verifier.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// The test outlives the synthetic request. Preserve only its verified values;
		// each operation still installs its own bounded context.
		ctx = context.WithoutCancel(r.Context())
	})).ServeHTTP(httptest.NewRecorder(), req)
	if ctx == nil {
		t.Fatal("test token did not verify")
	}
	return ctx
}
