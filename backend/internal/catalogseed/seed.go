// Package catalogseed loads development-only sample products without updating existing rows.
package catalogseed

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed products.json
var fixture string

type Product struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Category       string `json:"category"`
	PackageLabel   string `json:"package_label"`
	ImageReference string `json:"image_reference"`
	PriceCents     *int64 `json:"price_cents"`
	Currency       string `json:"currency"`
	Available      *bool  `json:"available"`
}

func Load() ([]Product, error) { return Decode(strings.NewReader(fixture)) }

func Decode(r io.Reader) ([]Product, error) {
	var products []Product
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&products); err != nil {
		return nil, errors.New("invalid product fixture JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("unexpected trailing fixture content")
	}
	if err := Validate(products); err != nil {
		return nil, err
	}
	return products, nil
}

func Validate(products []Product) error {
	if len(products) != 100 {
		return errors.New("fixture must contain exactly 100 products")
	}
	seen := make(map[[16]byte]bool)
	for i, p := range products {
		invalid := func(field string) error { return fmt.Errorf("fixture product %d: invalid %s", i+1, field) }
		var id pgtype.UUID
		if err := id.Scan(p.ID); err != nil || !id.Valid || id.Bytes == [16]byte{} {
			return invalid("id")
		}
		if seen[id.Bytes] {
			return invalid("duplicate id")
		}
		seen[id.Bytes] = true
		for _, field := range []struct{ name, value string }{{"name", p.Name}, {"description", p.Description}, {"package_label", p.PackageLabel}, {"image_reference", p.ImageReference}} {
			if strings.TrimSpace(field.value) == "" || strings.ContainsRune(field.value, 0) {
				return invalid(field.name)
			}
		}
		switch p.Category {
		case "produce", "dairy", "meat", "bakery", "pantry", "frozen":
		default:
			return invalid("category")
		}
		if p.Currency != "USD" {
			return invalid("currency")
		}
		if p.PriceCents == nil || *p.PriceCents < 0 || *p.PriceCents > 2147483647 {
			return invalid("price_cents")
		}
		if p.Available == nil || !*p.Available {
			return invalid("available")
		}
	}
	return nil
}

type Result struct{ Inserted, Skipped int }

func Seed(ctx context.Context, pool *pgxpool.Pool, products []Product) (Result, error) {
	if err := Validate(products); err != nil {
		return Result{}, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Result{}, errors.New("seed transaction could not start")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	result := Result{}
	for _, p := range products {
		tag, err := tx.Exec(ctx, `INSERT INTO products
   (id, name, description, category, package_label, image_reference, price_cents, currency, available)
   VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (id) DO NOTHING`,
			p.ID, p.Name, p.Description, p.Category, p.PackageLabel, p.ImageReference, *p.PriceCents, p.Currency, *p.Available)
		if err != nil {
			return Result{}, errors.New("seed insert failed; transaction rolled back")
		}
		result.Inserted += int(tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, errors.New("seed commit failed")
	}
	result.Skipped = len(products) - result.Inserted
	return result, nil
}
