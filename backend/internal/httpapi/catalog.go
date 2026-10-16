package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/WilliamSchweitzer/cartside/backend/internal/catalog"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type Catalog interface {
	List(context.Context, catalog.Query) (catalog.Page, error)
	Get(context.Context, string) (catalog.Product, error)
}

func catalogJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func catalogError(w http.ResponseWriter, status int, message string) {
	catalogJSON(w, status, map[string]string{"error": message})
}

func catalogQuery(raw string) (catalog.Query, error) {
	q := catalog.Query{Limit: 20}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return q, err
	}
	for _, key := range []string{"q", "limit", "offset"} {
		if v, ok := values[key]; ok && len(v) != 1 {
			return q, errors.New("repeated parameter")
		}
	}
	q.Search = strings.TrimSpace(values.Get("q"))
	if !utf8.ValidString(q.Search) || utf8.RuneCountInString(q.Search) > 200 || strings.ContainsRune(q.Search, 0) {
		return q, errors.New("invalid search")
	}
	q.Search = catalog.NormalizeSearch(q.Search)
	for _, field := range []struct {
		key      string
		target   *int
		min, max int
	}{
		{"limit", &q.Limit, 1, 100}, {"offset", &q.Offset, 0, 2147483647},
	} {
		if v, ok := values[field.key]; ok {
			// Accept only unsigned decimal digits, without signs or whitespace.
			if v[0] == "" || strings.IndexFunc(v[0], func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
				return q, errors.New("invalid number")
			}
			n, err := strconv.ParseInt(v[0], 10, 32)
			if err != nil || n < int64(field.min) || n > int64(field.max) {
				return q, errors.New("invalid range")
			}
			*field.target = int(n)
		}
	}
	return q, nil
}

func listProducts(repo Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q, err := catalogQuery(r.URL.RawQuery)
		if err != nil {
			catalogError(w, 400, "invalid query parameters")
			return
		}
		page, err := repo.List(r.Context(), q)
		if err != nil {
			catalogError(w, 503, "catalog unavailable")
			return
		}
		if page.Items == nil {
			page.Items = []catalog.Product{}
		}
		catalogJSON(w, 200, page)
	}
}

func getProduct(repo Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		var uuid pgtype.UUID
		if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || uuid.Scan(id) != nil || !uuid.Valid {
			catalogError(w, 400, "invalid product id")
			return
		}
		p, err := repo.Get(r.Context(), id)
		if errors.Is(err, catalog.ErrNotFound) {
			catalogError(w, 404, "product not found")
			return
		}
		if err != nil {
			catalogError(w, 503, "catalog unavailable")
			return
		}
		catalogJSON(w, 200, p)
	}
}
