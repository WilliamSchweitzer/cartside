//go:build integration

package orders

import (
	"context"
	"errors"
	"fmt"
	"github.com/WilliamSchweitzer/cartside/backend/internal/cart"
	"github.com/WilliamSchweitzer/cartside/backend/internal/catalog"
	"github.com/WilliamSchweitzer/cartside/backend/internal/database"
	"github.com/WilliamSchweitzer/cartside/backend/migrations"
	"github.com/jackc/pgx/v5/stdlib"
	"math"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestPostgresCheckout(t *testing.T) {
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
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatal("refusing nonempty database")
	}
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{apple, pear} {
		exec(`INSERT INTO products(id,name,description,category,package_label,image_reference,price_cents,currency) VALUES($1,'Apple','Fruit','produce','1 bag','/image.svg',125,'USD')`, id)
	}
	products := catalog.NewRepository(pool)
	carts := cart.NewService(pool, products)
	s := NewService(pool, carts, products)
	count := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM orders").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	read := func(owner context.Context) cart.Cart {
		t.Helper()
		c, err := carts.Get(owner)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	setup := func(subject string) (context.Context, CheckoutInput) {
		t.Helper()
		owner := verifiedContext(t, "https://issuer.example/one", subject)
		if err := carts.Add(owner, apple, 2); err != nil {
			t.Fatal(err)
		}
		return owner, CheckoutInput{"key", 1, []ReviewedItem{{apple, 2, 125}}}
	}
	checkFailure := func(owner context.Context, in CheckoutInput, want error) {
		t.Helper()
		before, n := read(owner), count()
		_, err := s.Checkout(owner, in)
		if !errors.Is(err, want) {
			t.Fatalf("got %v want %v", err, want)
		}
		if !reflect.DeepEqual(before, read(owner)) || n != count() {
			t.Fatal("failure changed state")
		}
	}
	t.Run("success snapshots replay conflict and deletion", func(t *testing.T) {
		owner, in := setup("success")
		o, err := s.Checkout(owner, in)
		if err != nil {
			t.Fatal(err)
		}
		if o.ID == "" || o.Confirmation[:4] != "SIM-" || o.Kind != "simulated" || o.Currency != "USD" || o.TotalCents != 250 || o.CreatedAt.IsZero() || len(o.Items) != 1 || o.Items[0].Name != "Apple" || o.Items[0].PackageLabel != "1 bag" || o.Items[0].LineTotalCents != 250 {
			t.Fatalf("order %+v", o)
		}
		c := read(owner)
		if c.Version != 2 || len(c.Items) != 0 {
			t.Fatal(c)
		}
		replay, err := s.Checkout(owner, in)
		if err != nil || !reflect.DeepEqual(o, replay) {
			t.Fatalf("empty replay %+v %v", replay, err)
		}
		if err := carts.Add(owner, pear, 3); err != nil {
			t.Fatal(err)
		}
		exec(`UPDATE products SET name='Edited',package_label='new package',price_cents=300 WHERE id=$1`, apple)
		exec(`DELETE FROM products WHERE id=$1`, apple)
		before := read(owner)
		replay, err = s.Checkout(owner, in)
		if err != nil || !reflect.DeepEqual(o, replay) || !reflect.DeepEqual(before, read(owner)) {
			t.Fatalf("changed cart/catalog replay %+v %v", replay, err)
		}
		changed := in
		changed.ExpectedCartVersion++
		checkFailure(owner, changed, ErrIdempotencyConflict)
		if _, err := pool.Exec(ctx, `UPDATE order_items SET name='changed' WHERE order_id=$1`, o.ID); err == nil {
			t.Fatal("mutable snapshot")
		}
		exec(`INSERT INTO products(id,name,description,category,package_label,image_reference,price_cents,currency) VALUES($1,'Apple','Fruit','produce','1 bag','/image.svg',125,'USD')`, apple)
	})

	t.Run("multiple products canonical replay and database constraints", func(t *testing.T) {
		owner, in := setup("multi")
		if err := carts.Add(owner, pear, 3); err != nil {
			t.Fatal(err)
		}
		in.ExpectedCartVersion = 2
		in.Items = append([]ReviewedItem{{pear, 3, 125}}, in.Items...)
		o, err := s.Checkout(owner, in)
		if err != nil || o.TotalCents != 625 || len(o.Items) != 2 || o.Items[0].ProductID != apple {
			t.Fatal(o, err)
		}
		in.Items[0], in.Items[1] = in.Items[1], in.Items[0]
		replay, err := s.Checkout(owner, in)
		if err != nil || !reflect.DeepEqual(o, replay) {
			t.Fatal("reordered replay", replay, err)
		}
		mustFail := func(sql string, args ...any) {
			t.Helper()
			if _, err := pool.Exec(ctx, sql, args...); err == nil {
				t.Fatal("constraint accepted invalid order data")
			}
		}
		mustFail(`INSERT INTO orders(owner_issuer,owner_subject,currency,total_cents,idempotency_key,request_fingerprint) SELECT owner_issuer,owner_subject,currency,total_cents,idempotency_key,request_fingerprint FROM orders WHERE id=$1`, o.ID)
		mustFail(`INSERT INTO orders(owner_issuer,owner_subject,currency,total_cents,idempotency_key,request_fingerprint) SELECT owner_issuer,owner_subject,'EUR',total_cents,'bad-currency',request_fingerprint FROM orders WHERE id=$1`, o.ID)
		mustFail(`INSERT INTO orders(owner_issuer,owner_subject,currency,total_cents,idempotency_key,request_fingerprint) SELECT owner_issuer,owner_subject,currency,-1,'bad-total',request_fingerprint FROM orders WHERE id=$1`, o.ID)
		mustFail(`INSERT INTO order_items(order_id,product_id,name,package_label,quantity,unit_price_cents,line_total_cents) VALUES($1,'00000000-0000-0000-0000-000000000099','X','bag',2,10,21)`, o.ID)
		mustFail(`DELETE FROM orders WHERE id=$1`, o.ID)
		c := read(owner)
		if c.Version != 3 || len(c.Items) != 0 {
			t.Fatal(c)
		}
	})
	t.Run("preconditions", func(t *testing.T) {
		owner, in := setup("preconditions")
		bad := in
		bad.ExpectedCartVersion = 0
		checkFailure(owner, bad, ErrStaleVersion)
		bad = in
		bad.Items = []ReviewedItem{{pear, 2, 125}}
		checkFailure(owner, bad, ErrReviewedItems)
		bad = in
		bad.Items = []ReviewedItem{{apple, 1, 125}}
		checkFailure(owner, bad, ErrReviewedItems)
		bad = in
		bad.Items = append(append([]ReviewedItem(nil), in.Items...), ReviewedItem{pear, 1, 125})
		checkFailure(owner, bad, ErrReviewedItems)
		exec(`UPDATE products SET price_cents=126 WHERE id=$1`, apple)
		checkFailure(owner, in, ErrPriceChanged)
		exec(`UPDATE products SET price_cents=125,available=false WHERE id=$1`, apple)
		checkFailure(owner, in, ErrUnavailable)
		exec(`UPDATE products SET available=true WHERE id=$1`, apple)
		empty := verifiedContext(t, "https://issuer.example/one", "absent")
		checkFailure(empty, in, ErrEmptyCart)
		if err := carts.Add(empty, apple, 1); err != nil {
			t.Fatal(err)
		}
		if err := carts.Remove(empty, apple); err != nil {
			t.Fatal(err)
		}
		checkFailure(empty, in, ErrEmptyCart)
	})
	t.Run("rollback after order insert and cart delete", func(t *testing.T) {
		owner, in := setup("rollback")
		exec(`ALTER TABLE carts ADD CONSTRAINT test_checkout_failure CHECK(owner_subject <> 'rollback' OR version < 2)`)
		checkFailure(owner, in, ErrStorage)
		exec(`ALTER TABLE carts DROP CONSTRAINT test_checkout_failure`)
		if _, err := s.Checkout(owner, in); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("wide cents and version overflow rollback", func(t *testing.T) {
		owner, in := setup("overflow")
		exec(`UPDATE carts SET version=$1 WHERE owner_subject='overflow'`, int64(math.MaxInt64))
		in.ExpectedCartVersion = math.MaxInt64
		checkFailure(owner, in, ErrStorage)
		exec(`UPDATE carts SET version=1 WHERE owner_subject='overflow'`)
		in.ExpectedCartVersion = 1
		exec(`UPDATE products SET price_cents=$1 WHERE id=$2`, int64(math.MaxInt32), apple)
		in.Items[0].UnitPriceCents = math.MaxInt32
		o, err := s.Checkout(owner, in)
		if err != nil || o.TotalCents != int64(math.MaxInt32)*2 {
			t.Fatal(o, err)
		}
		exec(`UPDATE products SET price_cents=125 WHERE id=$1`, apple)
		// Money overflow cannot be reached with the catalog int32 and cart 1..99
		// constraints at practical row counts; exercise the same checked arithmetic.
		if _, _, err := addTotal(math.MaxInt64, 125, 2); !errors.Is(err, ErrOverflow) {
			t.Fatal(err)
		}
	})
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprintf("concurrent same key %v", same), func(t *testing.T) {
			owner, in := setup(fmt.Sprint("concurrent-", same))
			n := count()
			var wg sync.WaitGroup
			start := make(chan struct{})
			type result struct {
				o Order
				e error
			}
			results := make(chan result, 8)
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					request := in
					if !same {
						request.IdempotencyKey = fmt.Sprint("key-", i)
					}
					o, e := s.Checkout(owner, request)
					results <- result{o, e}
				}(i)
			}
			close(start)
			wg.Wait()
			close(results)
			success := 0
			id := ""
			for r := range results {
				if r.e == nil {
					success++
					if id != "" && id != r.o.ID {
						t.Fatal("multiple confirmations")
					}
					id = r.o.ID
				} else if same || !errors.Is(r.e, ErrEmptyCart) {
					t.Fatal(r.e)
				}
			}
			if count() != n+1 || (same && success != 8) || (!same && success != 1) {
				t.Fatal("unexpected concurrent results", success)
			}
		})
	}
	t.Run("identity pair isolates keys", func(t *testing.T) {
		n := count()
		for _, p := range [][2]string{{"https://issuer.example/one", "isolation"}, {"https://issuer.example/two", "isolation"}, {"https://issuer.example/one", "another"}} {
			owner := verifiedContext(t, p[0], p[1])
			if err := carts.Add(owner, apple, 2); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Checkout(owner, CheckoutInput{"key", 1, []ReviewedItem{{apple, 2, 125}}}); err != nil {
				t.Fatal(err)
			}
		}
		if count() != n+3 {
			t.Fatal("identity leaked")
		}
	})
}
