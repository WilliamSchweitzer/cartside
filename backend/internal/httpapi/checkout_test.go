package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/orders"
)

const checkoutID = "bb5ce88f-908c-5a99-8ae7-90b60b00ae1b"
const checkoutBody = `{"expected_cart_version":3,"items":[{"product_id":"` + checkoutID + `","quantity":2,"unit_price_cents":399}]}`

type checkoutStub struct {
	calls int
	input orders.CheckoutInput
	err   error
}

func (s *checkoutStub) Checkout(ctx context.Context, in orders.CheckoutInput) (orders.Order, error) {
	s.calls++
	s.input = in
	return orders.Order{ID: checkoutID, Confirmation: "SIM-example", Kind: "simulated", CreatedAt: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC), Currency: "USD", TotalCents: 798, Items: []orders.Item{{ProductID: checkoutID, Name: "Apples", PackageLabel: "bag", Quantity: 2, UnitPriceCents: 399, LineTotalCents: 798}}}, s.err
}
func checkoutRequest(ctx context.Context, body string) *http.Request {
	r := httptest.NewRequest("POST", "/checkout", strings.NewReader(body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "review-1")
	r.Header.Set("Origin", "http://localhost:9080")
	r.Header.Set("X-CSRF-Protection", "1")
	return r
}
func TestCheckoutParsingAndHeaders(t *testing.T) {
	ctx := checkoutContext(t, "https://issuer.example", "checkout-test")
	cases := []struct {
		name, body string
		mutate     func(*http.Request)
	}{
		{"unknown", strings.Replace(checkoutBody, `"expected_cart_version":3`, `"extra":0,"expected_cart_version":3`, 1), nil},
		{"duplicate root", strings.Replace(checkoutBody, `"expected_cart_version":3`, `"expected_cart_version":3,"expected_cart_version":3`, 1), nil},
		{"duplicate escaped", strings.Replace(checkoutBody, `"quantity":2`, `"quantity":2,"quanti\u0074y":2`, 1), nil},
		{"item unknown", strings.Replace(checkoutBody, `"quantity":2`, `"unknown":0,"quantity":2`, 1), nil},
		{"missing version", strings.Replace(checkoutBody, `"expected_cart_version":3,`, "", 1), nil},
		{"missing item field", strings.Replace(checkoutBody, `,"unit_price_cents":399`, "", 1), nil},
		{"trailing", checkoutBody + ` {}`, nil},
		{"null product", strings.Replace(checkoutBody, `"`+checkoutID+`"`, `null`, 1), nil},
		{"wrong product type", strings.Replace(checkoutBody, `"`+checkoutID+`"`, `12`, 1), nil},
		{"quantity too large", strings.Replace(checkoutBody, `"quantity":2`, `"quantity":100`, 1), nil},
		{"truncated", checkoutBody[:len(checkoutBody)-1], nil},
		{"null root", `null`, nil}, {"null items", `{"expected_cart_version":3,"items":null}`, nil},
		{"null item", `{"expected_cart_version":3,"items":[null]}`, nil},
		{"empty items", `{"expected_cart_version":3,"items":[]}`, nil},
		{"bad uuid", strings.Replace(checkoutBody, checkoutID, "invalid", 1), nil},
		{"duplicate products", strings.Replace(checkoutBody, `}]`, `},{"product_id":"`+strings.ToUpper(checkoutID)+`","quantity":2,"unit_price_cents":399}]`, 1), nil},
		{"oversize", checkoutBody + strings.Repeat(" ", checkoutBodyLimit), nil},
		{"missing key", checkoutBody, func(r *http.Request) { r.Header.Del("Idempotency-Key") }},
		{"repeated key", checkoutBody, func(r *http.Request) { r.Header.Add("Idempotency-Key", "other") }},
		{"key spaces", checkoutBody, func(r *http.Request) { r.Header.Set("Idempotency-Key", "bad key") }},
		{"long key", checkoutBody, func(r *http.Request) { r.Header.Set("Idempotency-Key", strings.Repeat("a", 129)) }},
		{"no media", checkoutBody, func(r *http.Request) { r.Header.Del("Content-Type") }},
		{"wrong media", checkoutBody, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
		{"repeated media", checkoutBody, func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }},
	}
	for _, field := range []string{`"expected_cart_version":3`, `"quantity":2`, `"unit_price_cents":399`} {
		parts := strings.Split(field, ":")
		for _, value := range []string{"null", "1.0", "1e2", "-1", "9223372036854775808", `"3"`, "true"} {
			cases = append(cases, struct {
				name, body string
				mutate     func(*http.Request)
			}{field + value, strings.Replace(checkoutBody, field, parts[0]+":"+value, 1), nil})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &checkoutStub{}
			r := checkoutRequest(ctx, tc.body)
			if tc.mutate != nil {
				tc.mutate(r)
			}
			w := httptest.NewRecorder()
			checkoutHandler(s)(w, r)
			if w.Code != 400 || s.calls != 0 || !strings.Contains(w.Body.String(), `"code":"invalid_input"`) {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, s.calls, w.Body.String())
			}
		})
	}

	t.Run("large cart has no item count cap", func(t *testing.T) {
		items := make([]string, 2000)
		for i := range items {
			items[i] = fmt.Sprintf(`{"product_id":"00000000-0000-0000-0000-%012x","quantity":1,"unit_price_cents":1}`, i)
		}
		body := `{"expected_cart_version":1,"items":[` + strings.Join(items, ",") + `]}`
		s := &checkoutStub{}
		w := httptest.NewRecorder()
		checkoutHandler(s)(w, checkoutRequest(ctx, body))
		if w.Code != 200 || len(s.input.Items) != len(items) {
			t.Fatal(w.Code, len(s.input.Items))
		}
	})
	t.Run("wide integers", func(t *testing.T) {
		s := &checkoutStub{}
		body := strings.Replace(checkoutBody, `"expected_cart_version":3`, `"expected_cart_version":9007199254740993`, 1)
		body = strings.Replace(body, `"unit_price_cents":399`, `"unit_price_cents":9007199254740993`, 1)
		w := httptest.NewRecorder()
		checkoutHandler(s)(w, checkoutRequest(ctx, body))
		if w.Code != 200 || s.input.ExpectedCartVersion != 9007199254740993 || s.input.Items[0].UnitPriceCents != 9007199254740993 {
			t.Fatal(w.Code, s.input)
		}
	})
}
func TestCheckoutResponseAndErrors(t *testing.T) {
	ctx := checkoutContext(t, "https://issuer.example", "checkout-test")
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{orders.ErrUnauthenticated, 401, "unauthorized"}, {orders.ErrInvalidInput, 400, "invalid_input"}, {orders.ErrOverflow, 400, "invalid_input"},
		{orders.ErrEmptyCart, 409, "empty_cart"}, {orders.ErrStaleVersion, 409, "stale_cart_version"}, {orders.ErrReviewedItems, 409, "reviewed_items_mismatch"},
		{orders.ErrUnavailable, 409, "products_unavailable"}, {orders.ErrPriceChanged, 409, "prices_changed"}, {orders.ErrIdempotencyConflict, 409, "idempotency_conflict"},
		{orders.ErrStorage, 503, "checkout_unavailable"}, {context.DeadlineExceeded, 504, "checkout_timeout"}, {context.Canceled, 503, "checkout_unavailable"}, {errors.New("database secret"), 503, "checkout_unavailable"},
	} {
		t.Run(tc.code+tc.err.Error(), func(t *testing.T) {
			w := httptest.NewRecorder()
			checkoutHandler(&checkoutStub{err: tc.err})(w, checkoutRequest(ctx, checkoutBody))
			var body struct{ Code, Message string }
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if w.Code != tc.status || body.Code != tc.code || body.Message == "" || strings.Contains(w.Body.String(), "database secret") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	s := &checkoutStub{}
	w := httptest.NewRecorder()
	checkoutHandler(s)(w, checkoutRequest(ctx, checkoutBody))
	want := `{"id":"` + checkoutID + `","confirmation":"SIM-example","kind":"simulated","created_at":"2026-10-15T12:00:00Z","currency":"USD","total_cents":798,"items":[{"product_id":"` + checkoutID + `","name":"Apples","package_label":"bag","quantity":2,"unit_price_cents":399,"line_total_cents":798}]}` + "\n"
	if w.Code != 200 || w.Body.String() != want || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestCheckoutSecurity(t *testing.T) {
	_, authenticate, token := checkoutIdentity(t, "https://issuer.example", "checkout-test")
	csrf, _ := NewCSRF("http://localhost:9080")
	for _, tc := range []struct {
		name, token string
		csrf        bool
		status      int
	}{
		{"verified", token, true, 200}, {"missing csrf", token, false, 403}, {"anonymous", "", true, 401}, {"forged token", "forged", true, 401}, {"auth before csrf", "", false, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &checkoutStub{}
			router := NewRouterWithCheckout(nil, authenticate, nil, csrf, nil, s)
			r := checkoutRequest(context.Background(), checkoutBody)
			r.Header.Set("X-Access-Token", tc.token)
			r.Header.Set("X-User-ID", "forged-owner")
			if !tc.csrf {
				r.Header.Del("X-CSRF-Protection")
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != tc.status || (s.calls != 0 && tc.status != 200) {
				t.Fatal(w.Code, w.Body.String(), s.calls)
			}
			if tc.status == 401 && !strings.Contains(w.Body.String(), `"code":"unauthorized"`) {
				t.Fatal(w.Body.String())
			}
			if tc.status == 403 && w.Body.String() != "{\"error\":\"CSRF protection required\"}\n" {
				t.Fatal(w.Body.String())
			}
		})
	}
	w := httptest.NewRecorder()
	checkoutHandler(&checkoutStub{})(w, checkoutRequest(context.Background(), checkoutBody))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

func TestCheckoutRejectionEvidence(t *testing.T) {
	ctx := checkoutContext(t, "https://issuer.example", "rejection-test")
	for _, err := range []error{orders.ErrEmptyCart, orders.ErrStaleVersion, orders.ErrReviewedItems, orders.ErrUnavailable, orders.ErrPriceChanged, orders.ErrStorage, orders.ErrIdempotencyConflict, orders.ErrInvalidInput} {
		w := httptest.NewRecorder()
		checkoutHandler(&checkoutStub{err: err})(w, checkoutRequest(ctx, checkoutBody))
		want := w.Code == http.StatusConflict && !errors.Is(err, orders.ErrIdempotencyConflict)
		if got := w.Header().Get("X-Cartside-Checkout-Rejection") == "no-matching-order"; got != want {
			t.Fatalf("error %v: rejection evidence=%v want=%v", err, got, want)
		}
	}
}
