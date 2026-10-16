package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/WilliamSchweitzer/cartside/backend/internal/auth"
	"github.com/WilliamSchweitzer/cartside/backend/internal/cart"
	"github.com/WilliamSchweitzer/cartside/backend/internal/catalog"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type Cart interface {
	Get(context.Context) (cart.Cart, error)
	Add(context.Context, string, int) error
	SetQuantity(context.Context, string, int) error
	Remove(context.Context, string) error
}

type cartItemResponse struct {
	Product        catalog.Product `json:"product"`
	Quantity       int             `json:"quantity"`
	LineTotalCents int64           `json:"line_total_cents"`
}
type cartResponse struct {
	Version       int64              `json:"version"`
	Items         []cartItemResponse `json:"items"`
	SubtotalCents int64              `json:"subtotal_cents"`
	Currency      string             `json:"currency"`
}

func cartRoutes(r chi.Router, service Cart) {
	r.Route("/cart", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				if _, ok := auth.FromContext(req.Context()); !ok {
					catalogError(w, 401, "unauthorized")
					return
				}
				next.ServeHTTP(w, req)
			})
		})
		r.Get("/", func(w http.ResponseWriter, req *http.Request) {
			result, err := service.Get(req.Context())
			if err != nil {
				cartError(w, err)
				return
			}
			response := cartResponse{Version: result.Version, Items: make([]cartItemResponse, 0, len(result.Items)), SubtotalCents: result.SubtotalCents, Currency: result.Currency}
			for _, item := range result.Items {
				response.Items = append(response.Items, cartItemResponse{item.Product, item.Quantity, item.LineTotalCents})
			}
			catalogJSON(w, 200, response)
		})
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			path := "/items/{product_id}"
			if method == http.MethodPost {
				path = "/items"
			}
			r.MethodFunc(method, path, func(w http.ResponseWriter, req *http.Request) {
				id := chi.URLParam(req, "product_id")
				quantity := 0
				if req.Method != http.MethodDelete {
					mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
					if err != nil || mediaType != "application/json" || len(req.Header.Values("Content-Type")) != 1 {
						catalogError(w, 415, "application/json required")
						return
					}
					idFromBody, q, err := decodeCartBody(w, req, req.Method == http.MethodPost)
					if err != nil {
						var tooLarge *http.MaxBytesError
						if errors.As(err, &tooLarge) {
							catalogError(w, 413, "request body too large")
						} else {
							catalogError(w, 400, "invalid cart request")
						}
						return
					}
					quantity = q
					if req.Method == http.MethodPost {
						id = idFromBody
					}
				}
				var uuid pgtype.UUID
				if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || uuid.Scan(id) != nil || !uuid.Valid {
					catalogError(w, 400, "invalid product id")
					return
				}
				var err error
				switch req.Method {
				case http.MethodPost:
					err = service.Add(req.Context(), id, quantity)
				case http.MethodPut:
					err = service.SetQuantity(req.Context(), id, quantity)
				case http.MethodDelete:
					err = service.Remove(req.Context(), id)
				}
				if err != nil {
					cartError(w, err)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			})
		}
	})
}

func decodeCartBody(w http.ResponseWriter, r *http.Request, addition bool) (string, int, error) {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	invalid := errors.New("invalid cart JSON")
	token, err := d.Token()
	if err != nil {
		return "", 0, err
	}
	if token != json.Delim('{') {
		return "", 0, invalid
	}
	seen := map[string]bool{}
	var id string
	var quantity int
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return "", 0, err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return "", 0, invalid
		}
		seen[key] = true
		switch key {
		case "product_id":
			if !addition {
				return "", 0, invalid
			}
			var value *string
			if err := d.Decode(&value); err != nil {
				return "", 0, err
			}
			if value == nil {
				return "", 0, invalid
			}
			id = *value
		case "quantity":
			var value *int
			if err := d.Decode(&value); err != nil {
				return "", 0, err
			}
			if value == nil {
				return "", 0, invalid
			}
			quantity = *value
		default:
			return "", 0, invalid
		}
	}
	if _, err := d.Token(); err != nil {
		return "", 0, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		if err != nil {
			return "", 0, err
		}
		return "", 0, invalid
	}
	if !seen["quantity"] || quantity < 1 || quantity > 99 || (addition && !seen["product_id"]) {
		return "", 0, invalid
	}
	return id, quantity, nil
}

func cartError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, cart.ErrUnauthenticated):
		catalogError(w, 401, "unauthorized")
	case errors.Is(err, cart.ErrInvalidProductID), errors.Is(err, cart.ErrQuantity):
		catalogError(w, 400, "invalid cart request")
	case errors.Is(err, cart.ErrItemNotFound):
		catalogError(w, 404, "cart item not found")
	case errors.Is(err, catalog.ErrNotFound):
		catalogError(w, 404, "product not found")
	case errors.Is(err, cart.ErrUnavailable):
		catalogError(w, 409, "product unavailable")
	default:
		catalogError(w, 503, "cart unavailable")
	}
}
