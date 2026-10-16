//go:build integration

package catalogseed

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/database"
	"github.com/WilliamSchweitzer/cartside/backend/migrations"
	"github.com/jackc/pgx/v5/stdlib"
)

// This test requires a dedicated EMPTY disposable database, never the application DB.
func TestCatalogLifecycle(t *testing.T) {
	url := os.Getenv("CARTSIDE_TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("CARTSIDE_TEST_DATABASE_URL must point to an empty disposable database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := database.NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var tables int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema')`).Scan(&tables); err != nil {
		t.Fatal("cannot inspect disposable database")
	}
	if tables != 0 {
		t.Fatal("refusing integration test: database is not empty")
	}
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	// Also exercise migration and rollback with a pre-existing shared extension.
	if _, err := pool.Exec(ctx, "CREATE EXTENSION pg_trgm"); err != nil {
		t.Fatal(err)
	}
	results, err := provider.Up(ctx)
	if err != nil || len(results) != 4 {
		t.Fatalf("initial migration: %v, %v", results, err)
	}
	results, err = provider.Up(ctx)
	if err != nil || len(results) != 0 {
		t.Fatalf("repeat migration: %v, %v", results, err)
	}
	products, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	seed := func(want Result) {
		t.Helper()
		got, err := Seed(ctx, pool, products)
		if err != nil || got != want {
			t.Fatalf("seed: %+v, %v; want %+v", got, err, want)
		}
	}
	count := func(want int) {
		t.Helper()
		var got int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM products").Scan(&got); err != nil || got != want {
			t.Fatalf("count=%d err=%v want=%d", got, err, want)
		}
	}
	seed(Result{Inserted: 100})
	count(100)
	seed(Result{Skipped: 100})
	count(100)
	if _, err := pool.Exec(ctx, `UPDATE products SET price_cents=12345, available=false WHERE id=$1`, products[0].ID); err != nil {
		t.Fatal(err)
	}
	// A nonfixture record must also survive every reseed.
	if _, err := pool.Exec(ctx, `INSERT INTO products (name,description,category,package_label,image_reference,price_cents,currency) VALUES ('Extra','Custom product','pantry','1 jar','/local.svg',0,'USD')`); err != nil {
		t.Fatal(err)
	}
	seed(Result{Skipped: 100})
	count(101)
	var price int
	var available bool
	if err := pool.QueryRow(ctx, `SELECT price_cents,available FROM products WHERE id=$1`, products[0].ID).Scan(&price, &available); err != nil || price != 12345 || available {
		t.Fatalf("edits not preserved: %d %v %v", price, available, err)
	}
	if _, err := provider.Down(ctx); err != nil {
		t.Fatal(err)
	}
	// Orders roll back independently of carts and catalog.
	count(101)
	if _, err := provider.Down(ctx); err != nil {
		t.Fatal(err)
	}
	// The cart migration owns only cart tables; catalog data must survive.
	count(101)
	if _, err := provider.Down(ctx); err != nil {
		t.Fatal(err)
	}
	// Rolling back search must preserve the shared extension and catalog data.
	var installed bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT FROM pg_extension WHERE extname='pg_trgm')").Scan(&installed); err != nil || !installed {
		t.Fatalf("extension removed by rollback: %v", err)
	}
	count(101)
	if _, err := provider.Down(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	count(0)
	// Force a DB-only constraint failure on the final row, after 99 attempted inserts.
	if _, err := pool.Exec(ctx, `ALTER TABLE products ADD CONSTRAINT test_reject_last CHECK (name <> '`+strings.ReplaceAll(products[99].Name, "'", "''")+`')`); err != nil {
		t.Fatal(err)
	}
	if result, err := Seed(ctx, pool, products); err == nil || result != (Result{}) {
		t.Fatalf("expected atomic failure: %+v %v", result, err)
	}
	count(0)
	if _, err := pool.Exec(ctx, `ALTER TABLE products DROP CONSTRAINT test_reject_last`); err != nil {
		t.Fatal(err)
	}
	seed(Result{Inserted: 100})
	count(100)
}
