// Package catalog provides read-only access to authoritative product data.
package catalog

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("product not found")

type Product struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Category       string `json:"category"`
	PackageLabel   string `json:"package_label"`
	ImageReference string `json:"image_reference"`
	PriceCents     int32  `json:"price_cents"`
	Currency       string `json:"currency"`
	Available      bool   `json:"available"`
}

type Query struct {
	Search string
	Limit  int
	Offset int
}

type Page struct {
	Items   []Product `json:"items"`
	Limit   int       `json:"limit"`
	Offset  int       `json:"offset"`
	HasMore bool      `json:"has_more"`
}

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const columns = "id::text, name, description, category, package_label, image_reference, price_cents, currency, available"

func scan(row pgx.Row) (Product, error) {
	var p Product
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.Category, &p.PackageLabel, &p.ImageReference, &p.PriceCents, &p.Currency, &p.Available)
	return p, err
}

// List fetches one lookahead row, without a count query. strpos treats all search
// characters literally, including SQL LIKE wildcards and backslashes.
func (r *Repository) List(ctx context.Context, q Query) (Page, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	page := Page{Items: make([]Product, 0), Limit: q.Limit, Offset: q.Offset}
	rows, err := r.pool.Query(ctx, "SELECT "+columns+` FROM products
 WHERE strpos(lower(name), lower($1::text)) > 0 OR strpos(lower(description), lower($1::text)) > 0
 ORDER BY name, id LIMIT $2 OFFSET $3`, q.Search, q.Limit+1, q.Offset)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return Page{}, err
		}
		page.Items = append(page.Items, p)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if len(page.Items) > q.Limit {
		page.HasMore = true
		page.Items = page.Items[:q.Limit]
	}
	return page, nil
}

func (r *Repository) Get(ctx context.Context, id string) (Product, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	p, err := scan(r.pool.QueryRow(ctx, "SELECT "+columns+" FROM products WHERE id=$1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Product{}, ErrNotFound
	}
	return p, err
}
