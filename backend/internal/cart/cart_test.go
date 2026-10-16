package cart

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/auth"
	"github.com/WilliamSchweitzer/cartside/backend/internal/catalog"
	jose "github.com/go-jose/go-jose/v4"
)

func TestQuantityRules(t *testing.T) {
	for _, tc := range []struct {
		name               string
		current, requested int
		op                 mutation
		want               int
		invalid            bool
	}{
		{"first addition", 0, 1, add, 1, false},
		{"increment", 2, 3, add, 5, false},
		{"maximum", 98, 1, add, 99, false},
		{"over maximum", 99, 1, add, 0, true},
		{"zero addition", 2, 0, add, 0, true},
		{"negative addition", 2, -1, add, 0, true},
		{"huge addition", 2, math.MaxInt, add, 0, true},
		{"absolute increase", 2, 99, set, 99, false},
		{"absolute reduction", 99, 1, set, 1, false},
		{"same quantity", 2, 2, set, 2, false},
		{"zero set", 2, 0, set, 0, true},
		{"negative set", 2, -1, set, 0, true},
		{"over maximum set", 1, 100, set, 0, true},
		{"remove", 99, 0, remove, 0, false},
		{"repeated remove", 0, 0, remove, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nextQuantity(tc.current, tc.requested, tc.op)
			if got != tc.want || errors.Is(err, ErrQuantity) != tc.invalid {
				t.Fatalf("got %d, %v", got, err)
			}
		})
	}
}

func TestAvailabilityRules(t *testing.T) {
	for _, tc := range []struct {
		current, next     int
		available, reject bool
	}{
		{0, 1, false, true}, {1, 2, false, true}, {2, 1, false, false},
		{2, 0, false, false}, {2, 2, false, false}, {0, 99, true, false},
	} {
		if err := checkAvailability(tc.current, tc.next, tc.available); errors.Is(err, ErrUnavailable) != tc.reject {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

func TestTotalsUseWideIntegerCentsAndIncludeUnavailable(t *testing.T) {
	cart := Cart{Items: []Item{
		{Product: catalog.Product{PriceCents: math.MaxInt32, Available: true}, Quantity: 99},
		{Product: catalog.Product{PriceCents: math.MaxInt32, Available: false}, Quantity: 99},
		{Product: catalog.Product{PriceCents: 0}, Quantity: 5},
	}}
	if err := calculateTotals(&cart); err != nil {
		t.Fatal(err)
	}
	if cart.Items[0].LineTotalCents != int64(math.MaxInt32)*99 || cart.SubtotalCents != int64(math.MaxInt32)*198 {
		t.Fatalf("totals: %+v", cart)
	}
	if err := calculateTotals(&cart); err != nil || cart.SubtotalCents != int64(math.MaxInt32)*198 {
		t.Fatal("totals accumulated across calls")
	}
}

func TestPrincipalRequiredAndInputValidation(t *testing.T) {
	s := &Service{} // These failures must not reach a database.
	ctx := context.Background()
	if _, err := s.Get(ctx); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
	for _, err := range []error{s.Add(ctx, "anything", 1), s.SetQuantity(ctx, "anything", 1), s.Remove(ctx, "anything")} {
		if !errors.Is(err, ErrUnauthenticated) {
			t.Fatal(err)
		}
	}
	ctx = verifiedContext(t, "https://identity.example/realm", "alice")
	for _, err := range []error{s.Add(ctx, "bad", 1), s.SetQuantity(ctx, "bad", 1), s.Remove(ctx, "bad")} {
		if !errors.Is(err, ErrInvalidProductID) {
			t.Fatal(err)
		}
	}
	id := "00000000-0000-0000-0000-000000000001"
	for _, err := range []error{s.Add(ctx, id, 0), s.Add(ctx, id, 100), s.SetQuantity(ctx, id, -1), s.SetQuantity(ctx, id, 100)} {
		if !errors.Is(err, ErrQuantity) {
			t.Fatal(err)
		}
	}
}

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
