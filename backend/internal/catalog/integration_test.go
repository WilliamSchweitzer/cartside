//go:build integration

package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/database"
	"github.com/WilliamSchweitzer/cartside/backend/migrations"
	"github.com/jackc/pgx/v5/stdlib"
)

// Requires its own empty disposable database; never shares the seed lifecycle DB.
func TestPostgresCatalog(t *testing.T) {
	raw := os.Getenv("CARTSIDE_TEST_DATABASE_URL")
	if raw == "" {
		t.Fatal("CARTSIDE_TEST_DATABASE_URL must point to an empty disposable database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
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
	if _, err = provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for i, p := range []struct {
		name, description string
		available         bool
	}{
		{"Apple", "Crunchy fruit", true},
		{"Apple", "Second fruit", false},
		{"Banana", "APPLE in description", true},
		{"Literal 50%_off", "Backslash \\ deal", true},
		{"Literal 50xxoff", "Ordinary", true},
	} {
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", i+1)
		ids = append(ids, id)
		_, err := pool.Exec(ctx, `INSERT INTO products (id,name,description,category,package_label,image_reference,price_cents,currency,available)
   VALUES ($1,$2,$3,'produce','1 bag','/local.svg',499,'USD',$4)`, id, p.name, p.description, p.available)
		if err != nil {
			t.Fatal(err)
		}
	}
	repo := NewRepository(pool)
	for _, tc := range []struct {
		name, search  string
		limit, offset int
		want          []string
		more          bool
	}{
		{"ordered", "", 20, 0, ids, false},
		{"case and both fields", "aPpLe", 20, 0, ids[:3], false},
		{"description substring", "UNCH", 20, 0, ids[:1], false},
		{"percent", "%", 20, 0, ids[3:4], false},
		{"underscore", "_", 20, 0, ids[3:4], false},
		{"backslash", "\\", 20, 0, ids[3:4], false},
		{"injection literal", "' OR true --", 20, 0, []string{}, false},
		{"first page", "", 2, 0, ids[:2], true},
		{"middle page", "", 2, 2, ids[2:4], true},
		{"last page", "", 2, 4, ids[4:], false},
		{"exact page", "", 5, 0, ids, false},
		{"past end", "", 2, 5, []string{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := repo.List(ctx, Query{Search: tc.search, Limit: tc.limit, Offset: tc.offset})
			if err != nil {
				t.Fatal(err)
			}
			got := []string{}
			for _, p := range page.Items {
				got = append(got, p.ID)
			}
			if !reflect.DeepEqual(got, tc.want) || page.Items == nil || page.HasMore != tc.more || page.Limit != tc.limit || page.Offset != tc.offset {
				t.Fatalf("page %+v IDs %v want %v", page, got, tc.want)
			}
		})
	}
	p, err := repo.Get(ctx, ids[1])
	if err != nil || p.Available || p.PriceCents != 499 || p.Name != "Apple" || p.Currency != "USD" {
		t.Fatalf("detail %+v %v", p, err)
	}
	page, err := repo.List(ctx, Query{Limit: 20})
	if err != nil || page.Items[1] != p {
		t.Fatal("list/detail mismatch or unavailable product omitted")
	}
	if _, err := repo.Get(ctx, "00000000-0000-0000-0000-000000000099"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	pool.Close()
	if _, err := repo.Get(ctx, ids[0]); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatal("database failure reported as not-found")
	}
	if _, err := repo.List(ctx, Query{Limit: 20}); err == nil {
		t.Fatal("database failure ignored")
	}
}
