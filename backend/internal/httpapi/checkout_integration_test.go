//go:build integration

package httpapi

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/WilliamSchweitzer/cartside/backend/internal/cart"
	"github.com/WilliamSchweitzer/cartside/backend/internal/catalog"
	"github.com/WilliamSchweitzer/cartside/backend/internal/database"
	"github.com/WilliamSchweitzer/cartside/backend/internal/orders"
	"github.com/WilliamSchweitzer/cartside/backend/migrations"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresCheckoutHTTP(t *testing.T) {
	raw := os.Getenv("CARTSIDE_TEST_DATABASE_URL")
	if raw == "" {
		t.Fatal("requires empty disposable CARTSIDE_TEST_DATABASE_URL")
	}
	pool, err := database.NewPool(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var tables int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema')`).Scan(&tables); err != nil || tables != 0 {
		t.Fatal("refusing nonempty or inaccessible database")
	}
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(context.Background(), `INSERT INTO products(id,name,description,category,package_label,image_reference,price_cents,currency) VALUES($1,'Apples','Fruit','produce','bag','/image.svg',399,'USD')`, checkoutID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := checkoutContext(t, "https://issuer.example", "checkout-integration")
	products := catalog.NewRepository(pool)
	carts := cart.NewService(pool, products)
	service := orders.NewService(pool, carts, products)
	if err := carts.Add(ctx, checkoutID, 2); err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(checkoutBody, `"expected_cart_version":3`, `"expected_cart_version":1`, 1)
	send := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		checkoutHandler(service)(w, checkoutRequest(ctx, body))
		return w
	}
	before, err := carts.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	failed := send(strings.Replace(body, `"unit_price_cents":399`, `"unit_price_cents":400`, 1))
	after, err := carts.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Code != 409 || !strings.Contains(failed.Body.String(), `"code":"prices_changed"`) || after.Version != before.Version || len(after.Items) != 1 {
		t.Fatal("failed checkout did not preserve cart", failed.Code, failed.Body.String())
	}
	first := send(body)
	replay := send(body)
	if first.Code != 200 || replay.Code != 200 || first.Body.String() != replay.Body.String() {
		t.Fatal("replay differs", first.Code, first.Body.String(), replay.Code, replay.Body.String())
	}
	cleared, err := carts.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.Items) != 0 || cleared.Version != before.Version+1 {
		t.Fatal("cart not cleared once", cleared)
	}
	conflict := send(strings.Replace(body, `"quantity":2`, `"quantity":1`, 1))
	if conflict.Code != 409 || !strings.Contains(conflict.Body.String(), `"code":"idempotency_conflict"`) {
		t.Fatal(conflict.Code, conflict.Body.String())
	}
}
