package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/auth"
	"github.com/WilliamSchweitzer/cartside/backend/internal/orders"
)

// Bound bytes, not the number of cart items. No additional item-count limit.
const checkoutBodyLimit = 16 << 20

type Checkout interface {
	Checkout(context.Context, orders.CheckoutInput) (orders.Order, error)
}

type checkoutItemResponse struct {
	ProductID      string `json:"product_id"`
	Name           string `json:"name"`
	PackageLabel   string `json:"package_label"`
	Quantity       int    `json:"quantity"`
	UnitPriceCents int64  `json:"unit_price_cents"`
	LineTotalCents int64  `json:"line_total_cents"`
}
type checkoutResponse struct {
	ID           string                 `json:"id"`
	Confirmation string                 `json:"confirmation"`
	Kind         string                 `json:"kind"`
	CreatedAt    string                 `json:"created_at"`
	Currency     string                 `json:"currency"`
	TotalCents   int64                  `json:"total_cents"`
	Items        []checkoutItemResponse `json:"items"`
}

func checkoutHandler(service Checkout) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if _, ok := auth.FromContext(r.Context()); !ok {
			checkoutError(w, orders.ErrUnauthenticated)
			return
		}
		keys := r.Header.Values("Idempotency-Key")
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if len(keys) != 1 || len(r.Header.Values("Content-Type")) != 1 || err != nil || media != "application/json" {
			checkoutError(w, orders.ErrInvalidInput)
			return
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, checkoutBodyLimit))
		in, err := decodeCheckout(d)
		in.IdempotencyKey = keys[0]
		if err != nil || orders.ValidateInput(in) != nil {
			checkoutError(w, orders.ErrInvalidInput)
			return
		}
		result, err := service.Checkout(r.Context(), in)
		if err != nil {
			checkoutError(w, err)
			return
		}
		response := checkoutResponse{result.ID, result.Confirmation, result.Kind, result.CreatedAt.Format(time.RFC3339), result.Currency, result.TotalCents, make([]checkoutItemResponse, 0, len(result.Items))}
		for _, i := range result.Items {
			response.Items = append(response.Items, checkoutItemResponse{i.ProductID, i.Name, i.PackageLabel, i.Quantity, i.UnitPriceCents, i.LineTotalCents})
		}
		catalogJSON(w, http.StatusOK, response)
	}
}

// Decode each value directly into integers/strings; never round through float64.
func requiredValue(d *json.Decoder, target any) error {
	var raw json.RawMessage
	if err := d.Decode(&raw); err != nil {
		return err
	}
	if string(raw) == "null" {
		return orders.ErrInvalidInput
	}
	return json.Unmarshal(raw, target)
}

func strictObject(d *json.Decoder, fields map[string]func() error) error {
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return orders.ErrInvalidInput
	}
	seen := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		f := fields[name]
		if !ok || seen[name] || f == nil {
			return orders.ErrInvalidInput
		}
		seen[name] = true
		if err := f(); err != nil {
			return err
		}
	}
	if _, err := d.Token(); err != nil {
		return err
	}
	if len(seen) != len(fields) {
		return orders.ErrInvalidInput
	}
	return nil
}

func decodeCheckout(d *json.Decoder) (orders.CheckoutInput, error) {
	var in orders.CheckoutInput
	err := strictObject(d, map[string]func() error{
		"expected_cart_version": func() error { return requiredValue(d, &in.ExpectedCartVersion) },
		"items": func() error {
			token, err := d.Token()
			if err != nil || token != json.Delim('[') {
				return orders.ErrInvalidInput
			}
			for d.More() {
				var i orders.ReviewedItem
				if err := strictObject(d, map[string]func() error{
					"product_id":       func() error { return requiredValue(d, &i.ProductID) },
					"quantity":         func() error { return requiredValue(d, &i.Quantity) },
					"unit_price_cents": func() error { return requiredValue(d, &i.UnitPriceCents) },
				}); err != nil {
					return err
				}
				in.Items = append(in.Items, i)
			}
			_, err = d.Token()
			return err
		},
	})
	if err != nil {
		return in, err
	}
	var trailing json.RawMessage
	if err := d.Decode(&trailing); err != io.EOF {
		return in, orders.ErrInvalidInput
	}
	return in, nil
}

func checkoutError(w http.ResponseWriter, err error) {
	w.Header().Set("Cache-Control", "no-store")
	status, code, message := 503, "checkout_unavailable", "Checkout outcome may be unknown; retry with the exact same key and body."
	switch {
	case errors.Is(err, orders.ErrUnauthenticated):
		status, code, message = 401, "unauthorized", "Authentication required."
	case errors.Is(err, orders.ErrInvalidInput), errors.Is(err, orders.ErrOverflow):
		status, code, message = 400, "invalid_input", "Invalid checkout request."
	case errors.Is(err, orders.ErrEmptyCart):
		status, code, message = 409, "empty_cart", "Cart is empty."
	case errors.Is(err, orders.ErrStaleVersion):
		status, code, message = 409, "stale_cart_version", "Cart version changed; review the cart again."
	case errors.Is(err, orders.ErrReviewedItems):
		status, code, message = 409, "reviewed_items_mismatch", "Cart items changed; review the cart again."
	case errors.Is(err, orders.ErrUnavailable):
		status, code, message = 409, "products_unavailable", "A product is unavailable."
	case errors.Is(err, orders.ErrPriceChanged):
		status, code, message = 409, "prices_changed", "Prices changed; review the cart again."
	case errors.Is(err, orders.ErrIdempotencyConflict):
		status, code, message = 409, "idempotency_conflict", "Key was already used with different checkout input."
	case errors.Is(err, context.DeadlineExceeded):
		status, code = 504, "checkout_timeout"
	}
	catalogJSON(w, status, struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, message})
}

// Adapt authentication rejection for checkout without changing existing routes
// or the shared CSRF rejection body.
type checkoutAuthWriter struct {
	http.ResponseWriter
	unauthorized bool
}

func (w *checkoutAuthWriter) WriteHeader(status int) {
	if status == http.StatusUnauthorized {
		w.unauthorized = true
		checkoutError(w.ResponseWriter, orders.ErrUnauthenticated)
		return
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *checkoutAuthWriter) Write(p []byte) (int, error) {
	if w.unauthorized {
		return len(p), nil
	}
	return w.ResponseWriter.Write(p)
}
