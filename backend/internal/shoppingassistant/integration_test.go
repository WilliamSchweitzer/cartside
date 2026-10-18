//go:build integration

package shoppingassistant

import (
	"context"
	"os"
	"testing"

	"github.com/WilliamSchweitzer/cartside/backend/internal/catalog"
	"github.com/WilliamSchweitzer/cartside/backend/internal/catalogseed"
	"github.com/WilliamSchweitzer/cartside/backend/internal/database"
	"github.com/WilliamSchweitzer/cartside/backend/migrations"
	"github.com/jackc/pgx/v5/stdlib"
)

// Requires an empty disposable database, never developer data.
func TestPostgresAssistant(t *testing.T) {
	raw := os.Getenv("CARTSIDE_TEST_DATABASE_URL")
	if raw == "" {
		t.Fatal("requires empty disposable database")
	}
	ctx := context.Background()
	pool, e := database.NewPool(ctx, raw)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	var count int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema')`).Scan(&count); e != nil || count != 0 {
		t.Fatal("refusing nonempty database")
	}
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, e := migrations.NewProvider(db)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = provider.Up(ctx); e != nil {
		t.Fatal(e)
	}
	products, e := catalogseed.Load()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = catalogseed.Seed(ctx, pool, products); e != nil {
		t.Fatal(e)
	}
	// A pre-existing cart makes accidental mutation detectable.
	if _, e = pool.Exec(ctx, `INSERT INTO carts(owner_issuer,owner_subject,version) VALUES ('test','test',7)`); e != nil {
		t.Fatal(e)
	}
	snapshot := func() string {
		var s string
		e := pool.QueryRow(ctx, `SELECT json_build_array((SELECT json_agg(c) FROM carts c),(SELECT json_agg(i) FROM cart_items i),(SELECT json_agg(o) FROM orders o),(SELECT json_agg(i) FROM order_items i))::text`).Scan(&s)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	before := snapshot()
	a := New(catalog.NewRepository(pool), mockGateway(t))
	for scenario := range demoTerms {
		r, e := a.Suggest(ctx, scenario)
		if e != nil || len(r.Suggestions) == 0 || len(r.Suggestions) > 5 {
			t.Fatalf("%s: %+v %v", scenario, r, e)
		}
		for _, s := range r.Suggestions {
			p, e := catalog.NewRepository(pool).Get(ctx, s.ProductID)
			if e != nil || p.Name != s.Name || p.PriceCents != s.PriceCents || p.PackageLabel != s.Package || p.Currency != s.Currency || !s.Available {
				t.Fatal("not authoritative")
			}
		}
	}
	if snapshot() != before {
		t.Fatal("cart/order changes")
	}
}
