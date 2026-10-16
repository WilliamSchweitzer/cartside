//go:build integration

package cart

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/catalog"
	"github.com/WilliamSchweitzer/cartside/backend/internal/catalogseed"
	"github.com/WilliamSchweitzer/cartside/backend/internal/database"
	"github.com/WilliamSchweitzer/cartside/backend/migrations"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// Requires its own empty disposable PostgreSQL 17 database, never developer data.
func TestPostgresCart(t *testing.T) {
	raw := os.Getenv("CARTSIDE_TEST_DATABASE_URL")
	if raw == "" {
		t.Fatal("CARTSIDE_TEST_DATABASE_URL must point to an empty disposable database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := database.NewPool(ctx, raw)
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
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	products, err := catalogseed.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalogseed.Seed(ctx, pool, products); err != nil {
		t.Fatal(err)
	}
	s := NewService(pool, catalog.NewRepository(pool))
	apple, pear := products[0].ID, products[1].ID
	alice := verifiedContext(t, "https://issuer.example/one", "alice")
	bob := verifiedContext(t, "https://issuer.example/one", "bob")
	otherIssuer := verifiedContext(t, "https://issuer.example/two", "alice")
	read := func(t *testing.T, owner context.Context) Cart {
		t.Helper()
		c, err := s.Get(owner)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	requireErr := func(t *testing.T, err, want error) {
		t.Helper()
		if !errors.Is(err, want) {
			t.Fatalf("error %v, want %v", err, want)
		}
	}
	exec := func(t *testing.T, sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	countCarts := func(t *testing.T) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM carts").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	check := func(t *testing.T, owner context.Context, quantity int, version int64) Cart {
		t.Helper()
		c := read(t, owner)
		if c.Version != version {
			t.Fatalf("version %d want %d: %+v", c.Version, version, c)
		}
		if quantity == 0 {
			if len(c.Items) != 0 || c.SubtotalCents != 0 || c.Items == nil {
				t.Fatalf("not empty: %+v", c)
			}
		} else if len(c.Items) != 1 || c.Items[0].Quantity != quantity {
			t.Fatalf("quantity want %d: %+v", quantity, c)
		}
		return c
	}
	t.Run("empty reads and missing item operations do not create carts", func(t *testing.T) {
		check(t, alice, 0, 0)
		requireErr(t, s.Remove(alice, apple), nil)
		requireErr(t, s.SetQuantity(alice, apple, 1), ErrItemNotFound)
		requireErr(t, s.Add(alice, apple, 0), ErrQuantity)
		requireErr(t, s.Add(alice, "00000000-0000-0000-0000-000000000000", 1), catalog.ErrNotFound)
		exec(t, "UPDATE products SET available=false WHERE id=$1", apple)
		requireErr(t, s.Add(alice, apple, 1), ErrUnavailable)
		exec(t, "UPDATE products SET available=true WHERE id=$1", apple)
		if countCarts(t) != 0 {
			t.Fatal("failed/no-op operation persisted cart")
		}
	})
	t.Run("identity isolation and persistence", func(t *testing.T) {
		requireErr(t, s.Add(alice, apple, 2), nil)
		requireErr(t, s.Add(alice, apple, 3), nil)
		check(t, alice, 5, 2)
		check(t, bob, 0, 0)
		check(t, otherIssuer, 0, 0)
		requireErr(t, s.SetQuantity(bob, apple, 4), ErrItemNotFound)
		requireErr(t, s.Remove(otherIssuer, apple), nil)
		requireErr(t, s.Add(bob, pear, 1), nil)
		requireErr(t, s.Add(otherIssuer, apple, 7), nil)
		check(t, alice, 5, 2)
		check(t, bob, 1, 1)
		check(t, otherIssuer, 7, 1)
		if countCarts(t) != 3 {
			t.Fatal("identity pair not unique")
		}
		// Reopen the pool to prove that contents are persisted, not service-local.
		fresh, err := database.NewPool(ctx, raw)
		if err != nil {
			t.Fatal(err)
		}
		defer fresh.Close()
		got, err := NewService(fresh, catalog.NewRepository(fresh)).Get(alice)
		if err != nil || !reflect.DeepEqual(got, read(t, alice)) {
			t.Fatalf("persistence: %+v %v", got, err)
		}
	})
	t.Run("totals and current catalog price without reseeding", func(t *testing.T) {
		old := read(t, alice)
		exec(t, "UPDATE products SET price_cents=2147483647 WHERE id=$1", apple)
		got := check(t, alice, 5, 2)
		if got.SubtotalCents != int64(2147483647)*5 || got.Items[0].LineTotalCents != got.SubtotalCents || old.SubtotalCents == got.SubtotalCents {
			t.Fatalf("current price / width: %+v", got)
		}
		requireErr(t, s.Add(alice, pear, 2), nil)
		got = read(t, alice)
		if len(got.Items) != 2 || got.SubtotalCents != int64(2147483647)*5+*products[1].PriceCents*2 || got.Currency != "USD" {
			t.Fatalf("multi-line totals: %+v", got)
		}
		requireErr(t, s.Remove(alice, pear), nil)
		check(t, alice, 5, 4)
	})
	t.Run("unavailable reductions removal and no-ops", func(t *testing.T) {
		exec(t, "UPDATE products SET available=false WHERE id=$1", apple)
		c := check(t, alice, 5, 4)
		if c.Items[0].Product.Available || c.SubtotalCents != int64(2147483647)*5 {
			t.Fatal("unavailable item hidden or excluded from subtotal")
		}
		requireErr(t, s.Add(alice, apple, 1), ErrUnavailable)
		requireErr(t, s.SetQuantity(alice, apple, 6), ErrUnavailable)
		requireErr(t, s.SetQuantity(alice, apple, 5), nil)
		check(t, alice, 5, 4)
		requireErr(t, s.SetQuantity(alice, apple, 2), nil)
		check(t, alice, 2, 5)
		requireErr(t, s.Remove(alice, apple), nil)
		check(t, alice, 0, 6)
		requireErr(t, s.Remove(alice, apple), nil)
		check(t, alice, 0, 6)
		requireErr(t, s.SetQuantity(alice, apple, 1), ErrItemNotFound)
		requireErr(t, s.Add(alice, apple, 1), ErrUnavailable)
		check(t, alice, 0, 6)
		exec(t, "UPDATE products SET available=true WHERE id=$1", apple)
	})
	t.Run("quantity limits and failed changes preserve version", func(t *testing.T) {
		requireErr(t, s.Add(alice, apple, 99), nil)
		before := check(t, alice, 99, 7)
		for _, err := range []error{s.Add(alice, apple, 1), s.Add(alice, apple, 99), s.SetQuantity(alice, apple, 100), s.SetQuantity(alice, apple, 0)} {
			requireErr(t, err, ErrQuantity)
		}
		requireErr(t, s.SetQuantity(alice, apple, 99), nil)
		if !reflect.DeepEqual(before, read(t, alice)) {
			t.Fatal("failure or no-op changed cart")
		}
	})
	t.Run("concurrent first creation and increments", func(t *testing.T) {
		owner := verifiedContext(t, "https://issuer.example/one", "concurrent")
		parallel := func(n int, fn func() error) []error {
			t.Helper()
			start := make(chan struct{})
			results := make(chan error, n)
			var wg sync.WaitGroup
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); <-start; results <- fn() }()
			}
			close(start)
			wg.Wait()
			close(results)
			errs := []error{}
			for e := range results {
				errs = append(errs, e)
			}
			return errs
		}
		for _, err := range parallel(24, func() error { return s.Add(owner, apple, 1) }) {
			requireErr(t, err, nil)
		}
		check(t, owner, 24, 24)
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM carts WHERE owner_subject='concurrent'").Scan(&count); err != nil || count != 1 {
			t.Fatalf("first creation: %d %v", count, err)
		}
		for _, err := range parallel(24, func() error { return s.Add(owner, apple, 1) }) {
			requireErr(t, err, nil)
		}
		check(t, owner, 48, 48)
		requireErr(t, s.SetQuantity(owner, apple, 98), nil)
		successes, limited := 0, 0
		for _, err := range parallel(24, func() error { return s.Add(owner, apple, 1) }) {
			switch {
			case err == nil:
				successes++
			case errors.Is(err, ErrQuantity):
				limited++
			default:
				t.Fatal(err)
			}
		}
		if successes != 1 || limited != 23 {
			t.Fatalf("limit race: %d successes, %d limited", successes, limited)
		}
		check(t, owner, 99, 50)
	})
	t.Run("database failure rolls back item and version atomically", func(t *testing.T) {
		before := read(t, bob)
		exec(t, "ALTER TABLE carts ADD CONSTRAINT test_version_failure CHECK (owner_subject <> 'bob' OR version < 2)")
		if err := s.Add(bob, apple, 1); err == nil {
			t.Fatal("expected version update failure")
		}
		if !reflect.DeepEqual(before, read(t, bob)) {
			t.Fatal("item write survived failed version update")
		}
		exec(t, "ALTER TABLE carts DROP CONSTRAINT test_version_failure")
		// Failure after first-cart creation must also remove the provisional cart.
		exec(t, "ALTER TABLE cart_items ADD CONSTRAINT test_item_failure CHECK (product_id <> '"+apple+"'::uuid) NOT VALID")
		first := verifiedContext(t, "https://issuer.example/one", "rollback-first")
		beforeCount := countCarts(t)
		if err := s.Add(first, apple, 1); err == nil {
			t.Fatal("expected item write failure")
		}
		if countCarts(t) != beforeCount {
			t.Fatal("provisional cart survived rollback")
		}
		exec(t, "ALTER TABLE cart_items DROP CONSTRAINT test_item_failure")
		requireErr(t, s.Add(first, apple, 1), nil)
		check(t, first, 1, 1)
	})
	t.Run("constraints and product deletion restriction", func(t *testing.T) {
		mustFail := func(code, sql string, args ...any) {
			t.Helper()
			_, err := pool.Exec(ctx, sql, args...)
			var pgerr *pgconn.PgError
			if !errors.As(err, &pgerr) || pgerr.Code != code {
				t.Fatalf("want SQLSTATE %s, got %v", code, err)
			}
		}
		mustFail("23503", "DELETE FROM products WHERE id=$1", apple)
		check(t, alice, 99, 7)
		mustFail("23514", "UPDATE cart_items SET quantity=0")
		mustFail("23514", "UPDATE cart_items SET quantity=100")
		mustFail("23514", "UPDATE carts SET version=-1")
		mustFail("23514", "INSERT INTO carts(owner_issuer,owner_subject) VALUES ('','blank-owner')")
		mustFail("23505", `INSERT INTO carts(owner_issuer,owner_subject) VALUES ('https://issuer.example/one','alice')`)
		mustFail("23505", `INSERT INTO cart_items SELECT cart_id,product_id,quantity FROM cart_items LIMIT 1`)
		mustFail("23503", `INSERT INTO cart_items SELECT id,'00000000-0000-0000-0000-000000000000',1 FROM carts LIMIT 1`)
		mustFail("23503", `INSERT INTO cart_items VALUES ('00000000-0000-0000-0000-000000000000',$1,1)`, apple)
	})
	t.Run("cancellation while waiting for cart lock", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer rollback(tx)
		if _, err := tx.Exec(ctx, `SELECT id FROM carts WHERE owner_subject='bob' FOR UPDATE`); err != nil {
			t.Fatal(err)
		}
		before := read(t, bob)
		short, cancel := context.WithTimeout(bob, 100*time.Millisecond)
		defer cancel()
		if err := s.Add(short, pear, 1); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lock wait: %v", err)
		}
		rollback(tx)
		if !reflect.DeepEqual(before, read(t, bob)) {
			t.Fatal("cancelled operation changed cart")
		}
		requireErr(t, s.Add(bob, pear, 1), nil)
	})
	t.Run("availability checks wait for catalog updates and hold product locks", func(t *testing.T) {
		owner := verifiedContext(t, "https://issuer.example/one", "availability-race")
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer rollback(tx)
		if _, err := tx.Exec(ctx, "UPDATE products SET available=false WHERE id=$1", apple); err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() { result <- s.Add(owner, apple, 1) }()
		// Wait for the actual product SHARE lock attempt, not a timing-based guess.
		waitCtx, stop := context.WithTimeout(ctx, 2*time.Second)
		defer stop()
		for {
			var waiting bool
			err := pool.QueryRow(waitCtx, `SELECT EXISTS (SELECT FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND strpos(query,'FROM products WHERE id=$1 FOR SHARE')>0)`).Scan(&waiting)
			if err != nil {
				t.Fatal("mutation did not wait for product update", err)
			}
			if waiting {
				break
			}
			select {
			case <-waitCtx.Done():
				t.Fatal("mutation did not reach product lock")
			case <-time.After(10 * time.Millisecond):
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		requireErr(t, <-result, ErrUnavailable)
		check(t, owner, 0, 0)
		exec(t, "UPDATE products SET available=true WHERE id=$1", apple)

		tx, err = pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer rollback(tx)
		product, err := s.products.GetForShare(ctx, tx, apple)
		if err != nil || !product.Available {
			t.Fatalf("catalog lock: %+v %v", product, err)
		}
		short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		updateTx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer rollback(updateTx)
		if _, err := updateTx.Exec(short, "UPDATE products SET available=false WHERE id=$1", apple); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("availability changed while SHARE lock held: %v", err)
		}
		// Explicit rollback also rules out an autocommit racing cancellation.
		rollback(updateTx)
		rollback(tx)
		requireErr(t, s.Add(owner, apple, 1), nil)
		check(t, owner, 1, 1)
	})
	t.Run("rollback cart migration preserves catalog and search", func(t *testing.T) {
		// Remove the later orders migration before exercising cart rollback.
		if _, err := provider.Down(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := provider.Down(ctx); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM products").Scan(&count); err != nil || count != 100 {
			t.Fatalf("catalog damaged: %d %v", count, err)
		}
		if _, err := provider.Up(ctx); err != nil {
			t.Fatal(err)
		}
		check(t, alice, 0, 0)
	})
	t.Log("verified persistent ownership, quantities, current prices, concurrent writes, versions, rollback, and constraints on PostgreSQL")
}
