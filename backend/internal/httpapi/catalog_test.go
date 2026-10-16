package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/WilliamSchweitzer/cartside/backend/internal/catalog"
)

type catalogStub struct {
	query   catalog.Query
	id      string
	calls   int
	page    catalog.Page
	product catalog.Product
	err     error
}

func (s *catalogStub) List(_ context.Context, q catalog.Query) (catalog.Page, error) {
	s.calls++
	s.query = q
	if s.page.Limit == 0 {
		s.page.Limit = q.Limit
		s.page.Offset = q.Offset
	}
	return s.page, s.err
}
func (s *catalogStub) Get(_ context.Context, id string) (catalog.Product, error) {
	s.calls++
	s.id = id
	return s.product, s.err
}
func catalogRequest(repo Catalog, path string) *httptest.ResponseRecorder {
	router := NewRouter(func(context.Context) error { return nil }, func(h http.Handler) http.Handler { return h }, repo)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}
func TestCatalogValidation(t *testing.T) {
	for _, query := range []string{
		"limit=", "limit=0", "limit=101", "limit=-1", "limit=1.5", "limit=+1", "limit=abc", "limit=1&limit=2",
		"offset=", "offset=-1", "offset=2147483648", "offset=99999999999999999999999", "offset=1&offset=2",
		"q=" + url.QueryEscape("a"+strings.Repeat(" ", 200)+"b"),
		"q=a&q=b", "q=%zz", "q=%00", "q=%FF", "q=" + url.QueryEscape(strings.Repeat("界", 201)),
	} {
		t.Run(query, func(t *testing.T) {
			repo := &catalogStub{}
			w := catalogRequest(repo, "/products?"+query)
			if w.Code != 400 || repo.calls != 0 {
				t.Fatalf("status %d calls %d", w.Code, repo.calls)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing no-store")
			}
		})
	}
	for _, id := range []string{"bad", "bb5ce88fx908cx5a99x8ae7x90b60b00ae1b", "bb5ce88f908c5a998ae790b60b00ae1b", "zz5ce88f-908c-5a99-8ae7-90b60b00ae1b"} {
		repo := &catalogStub{}
		if w := catalogRequest(repo, "/products/"+id); w.Code != 400 || repo.calls != 0 {
			t.Fatalf("invalid ID accepted: %s", id)
		}
	}
}
func TestCatalogList(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  catalog.Query
	}{
		{"", catalog.Query{Limit: 20}},
		{"?q=%20%20%20", catalog.Query{Limit: 20}},
		{"?q=Whole%09%20%20Milk", catalog.Query{Search: "Whole Milk", Limit: 20}},
		{"?q=%20Milk%20&limit=100&offset=2147483647", catalog.Query{Search: "Milk", Limit: 100, Offset: 2147483647}},
		{"?q=" + url.QueryEscape(strings.Repeat("界", 200)), catalog.Query{Search: strings.Repeat("界", 200), Limit: 20}},
	} {
		repo := &catalogStub{}
		w := catalogRequest(repo, "/products"+tc.query)
		if w.Code != 200 || repo.query != tc.want {
			t.Fatalf("response %d query %+v", w.Code, repo.query)
		}
		var page catalog.Page
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.Items == nil || page.Limit != tc.want.Limit || page.Offset != tc.want.Offset || page.HasMore {
			t.Fatalf("invalid empty page: %s", w.Body.String())
		}
	}
	repo := &catalogStub{page: catalog.Page{Items: []catalog.Product{{ID: "one", PriceCents: 499}}, Limit: 1, Offset: 2, HasMore: true}}
	w := catalogRequest(repo, "/products?limit=1&offset=2")
	var page catalog.Page
	if json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].PriceCents != 499 || !page.HasMore || page.Offset != 2 {
		t.Fatal(w.Body.String())
	}
}
func TestCatalogDetailAndFailures(t *testing.T) {
	id := "bb5ce88f-908c-5a99-8ae7-90b60b00ae1b"
	p := catalog.Product{ID: id, Name: "Apples", Description: "Sweet", Category: "produce", PackageLabel: "1 bag", ImageReference: "/local.svg", PriceCents: 499, Currency: "USD", Available: false}
	repo := &catalogStub{product: p}
	w := catalogRequest(repo, "/products/"+id)
	var got catalog.Product
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got != p || repo.id != id {
		t.Fatal(w.Body.String())
	}
	for _, tc := range []struct {
		path string
		err  error
		code int
	}{
		{"/products/" + id, catalog.ErrNotFound, 404},
		{"/products/" + id, errors.New("password=secret"), 503},
		{"/products", errors.New("password=secret"), 503},
	} {
		w := catalogRequest(&catalogStub{err: tc.err}, tc.path)
		if w.Code != tc.code || strings.Contains(w.Body.String(), "secret") || w.Header().Get("Content-Type") != "application/json" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unexpected error: %d %s", w.Code, w.Body.String())
		}
	}
}
func TestCatalogRequiresAuthentication(t *testing.T) {
	repo := &catalogStub{}
	router := NewRouter(func(context.Context) error { return nil }, testVerifier(t).Middleware, repo)
	for _, path := range []string{"/products", "/products/bb5ce88f-908c-5a99-8ae7-90b60b00ae1b"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 || repo.calls != 0 {
			t.Fatalf("unprotected %s", path)
		}
	}
}
