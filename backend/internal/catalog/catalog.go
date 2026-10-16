// Package catalog provides read-only access to authoritative product data.
package catalog

import (
	"context"
	"errors"
	"strings"
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

// NormalizeSearch collapses Unicode whitespace for every repository caller.
func NormalizeSearch(search string) string { return strings.Join(strings.Fields(search), " ") }

// FuzzyThreshold is evaluated against the development fixture; see docs/catalog-search.md.
const FuzzyThreshold = 0.44

const listSQL = `WITH evidence AS (
 SELECT products.*,
   strpos(lower(name), lower($1::text)) AS name_position,
   strpos(lower(description), lower($1::text)) AS description_position,
   CASE WHEN char_length($1) >= 3 AND $5::boolean THEN word_similarity(lower($1), lower(name)) ELSE 0 END AS name_score,
   CASE WHEN char_length($1) >= 3 AND $5::boolean THEN word_similarity(lower($1), lower(description)) ELSE 0 END AS description_score
 FROM products
)
SELECT ` + columns + ` FROM evidence
 WHERE $1 = '' OR name_position > 0 OR description_position > 0
    OR name_score >= $4 OR description_score >= $4
 ORDER BY
   CASE WHEN $1 = '' THEN 0
        WHEN lower(name) = lower($1) THEN 0
        WHEN name_position = 1 THEN 1
        WHEN name_position > 0 THEN 2 ELSE 3 END,
   CASE WHEN $1 = '' THEN 0 ELSE name_score + 0.2 * description_score END DESC,
   name, id
 LIMIT $2 OFFSET $3`

// List fetches one lookahead row, without a count query. strpos treats all search
// characters literally, including SQL LIKE wildcards and backslashes.
func (r *Repository) List(ctx context.Context, q Query) (Page, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	page := Page{Items: make([]Product, 0), Limit: q.Limit, Offset: q.Offset}
	search := NormalizeSearch(q.Search)
	// Keep wildcard/backslash searches strictly literal: pg_trgm ignores punctuation.
	fuzzy := !strings.ContainsAny(search, `%_\`)
	rows, err := r.pool.Query(ctx, listSQL, search, q.Limit+1, q.Offset, FuzzyThreshold, fuzzy)
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
