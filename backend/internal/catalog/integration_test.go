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

	"github.com/WilliamSchweitzer/cartside/backend/internal/catalogseed"
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
		{"embedded wildcards", "%_off", 20, 0, ids[3:4], false},
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
	testSearchTiers(t, ctx, repo)
	testFixtureSearch(t, ctx, repo)
	pool.Close()
	if _, err := repo.Get(ctx, ids[0]); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatal("database failure reported as not-found")
	}
	if _, err := repo.List(ctx, Query{Limit: 20}); err == nil {
		t.Fatal("database failure ignored")
	}
}

func testFixtureSearch(t *testing.T, ctx context.Context, repo *Repository) {
	t.Helper()
	if _, err := repo.pool.Exec(ctx, "TRUNCATE products"); err != nil {
		t.Fatal(err)
	}
	products, err := catalogseed.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalogseed.Seed(ctx, repo.pool, products); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		query            string
		include, exclude []string
		first            string
	}{
		{"Gala Apples", []string{"Gala Apples"}, []string{"Bananas"}, "Gala Apples"},
		{"broc", []string{"Broccoli Crowns", "Broccoli Florets", "Brown Rice"}, []string{"Country White Bread"}, "Broccoli Crowns"},
		{"milk", []string{"Whole Milk", "Half and Half", "Buttermilk Waffles"}, []string{"Mixed Berries"}, "Reduced Fat Milk"},
		{"guacamole", []string{"Avocados"}, []string{"Gala Apples"}, "Avocados"},
		{"guacamle", []string{"Avocados"}, []string{"Gala Apples"}, "Avocados"},
		{"whole milk", []string{"Whole Milk", "Skim Milk"}, []string{"Bananas"}, "Whole Milk"},
		{"brocoli", []string{"Broccoli Crowns", "Broccoli Florets"}, []string{"Brown Rice", "Vegetable Broth"}, "Broccoli Crowns"},
		{"broccxli", []string{"Broccoli Crowns", "Broccoli Florets"}, []string{"Brown Rice"}, "Broccoli Crowns"},
		{"brococli", []string{"Broccoli Crowns", "Broccoli Florets"}, []string{"Brown Rice"}, "Broccoli Crowns"},
		{"chikcen breast", []string{"Chicken Breast"}, []string{"Chicken Thighs"}, "Chicken Breast"},
		{"  whole\t  mlk  ", []string{"Whole Milk", "Whole Chicken"}, []string{"Skim Milk"}, "Whole Milk"},
		{"mi", []string{"Skim Milk", "Mixed Berries"}, []string{"Gala Apples", "Butter Croissants"}, "Mild Italian Sausage"},
		{"m", []string{"Whole Milk"}, []string{"Gala Apples"}, "Mango Chunks"},
		{"aplpe", nil, []string{"Gala Apples"}, ""},
		{"xylophone", nil, []string{"Gala Apples"}, ""},
		{"%", nil, []string{"Gala Apples"}, ""},
		{"_", nil, []string{"Gala Apples"}, ""},
		{"%milk", nil, []string{"Whole Milk"}, ""},
		{"butter", []string{"Butter Croissants", "Salted Butter", "Buttermilk Waffles"}, []string{"Hamburger Buns"}, "Butter Croissants"},
	}
	for _, tc := range cases {
		t.Run("fixture/"+tc.query, func(t *testing.T) {
			page, err := repo.List(ctx, Query{Search: tc.query, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			names := []string{}
			positions := map[string]int{}
			seen := map[string]bool{}
			for i, p := range page.Items {
				names = append(names, p.Name)
				positions[p.Name] = i
				if seen[p.ID] {
					t.Fatal("duplicate", p.ID)
				}
				seen[p.ID] = true
			}
			t.Logf("%q: %v", tc.query, names)
			if tc.first == "" {
				if len(names) != 0 {
					t.Fatalf("expected no matches: %v", names)
				}
			} else if len(names) == 0 || names[0] != tc.first {
				t.Fatalf("first want %q: %v", tc.first, names)
			}
			for _, n := range tc.include {
				if _, ok := positions[n]; !ok {
					t.Errorf("missing %s", n)
				}
			}
			for _, n := range tc.exclude {
				if _, ok := positions[n]; ok {
					t.Errorf("unexpected %s", n)
				}
			}
			// One-row pages cross every relevance boundary and must reconstruct the same order.
			for i, p := range page.Items {
				small, err := repo.List(ctx, Query{Search: tc.query, Limit: 1, Offset: i})
				if err != nil || len(small.Items) != 1 || small.Items[0] != p || small.HasMore != (i+1 < len(page.Items)) {
					t.Fatalf("unstable page %d: %+v %v", i, small, err)
				}
			}
			for _, pair := range [][2]string{{"Whole Milk", "Half and Half"}, {"Whole Milk", "Buttermilk Waffles"}, {"Salted Butter", "Peanut Butter"}, {"Broccoli Florets", "Brown Rice"}} {
				a, aok := positions[pair[0]]
				b, bok := positions[pair[1]]
				if len(NormalizeSearch(tc.query)) >= 3 && aok && bok && a >= b {
					t.Errorf("%s must precede %s", pair[0], pair[1])
				}
			}
		})
	}
	if _, err := repo.pool.Exec(ctx, "ANALYZE products"); err != nil {
		t.Fatal(err)
	}
	var plan string
	rows, err := repo.pool.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+listSQL, "brocoli", 21, 0, FuzzyThreshold, true)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&plan); err != nil {
			t.Fatal(err)
		}
		t.Log(plan)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func testSearchTiers(t *testing.T, ctx context.Context, repo *Repository) {
	t.Helper()
	for i, name := range []string{"Apple Pie", "Crab Apple", "Applesauce", "Appel"} {
		_, err := repo.pool.Exec(ctx, `INSERT INTO products (id,name,description,category,package_label,image_reference,price_cents,currency) VALUES ($1,$2,'Fruit','produce','1 bag','/local.svg',100,'USD')`, fmt.Sprintf("00000000-0000-0000-0000-%012d", 10+i), name)
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := repo.List(ctx, Query{Search: "apple", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	positions := map[string]int{}
	for i, p := range page.Items {
		positions[p.Name] = i
	}
	if len(page.Items) < 7 || page.Items[0].ID != "00000000-0000-0000-0000-000000000001" || page.Items[1].ID != "00000000-0000-0000-0000-000000000002" {
		t.Fatalf("exact matches / ID tie break: %+v", page)
	}
	for _, pair := range [][2]string{{"Apple", "Apple Pie"}, {"Apple Pie", "Crab Apple"}, {"Applesauce", "Crab Apple"}, {"Crab Apple", "Banana"}, {"Crab Apple", "Appel"}, {"Appel", "Banana"}} {
		a, aok := positions[pair[0]]
		b, bok := positions[pair[1]]
		if !aok || !bok || a >= b {
			t.Fatalf("tier %v: %+v", pair, page)
		}
	}
	for i, p := range page.Items {
		one, err := repo.List(ctx, Query{Search: "apple", Limit: 1, Offset: i})
		if err != nil || len(one.Items) != 1 || one.Items[0] != p || one.HasMore != (i+1 < len(page.Items)) {
			t.Fatalf("tier page %d: %+v %v", i, one, err)
		}
	}
}
