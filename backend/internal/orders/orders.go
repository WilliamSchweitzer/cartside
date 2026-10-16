// Package orders coordinates simulated checkout. No payment, tax, shipping or
// delivery is performed. Ownership comes exclusively from verified context.
package orders

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/auth"
	"github.com/WilliamSchweitzer/cartside/backend/internal/cart"
	"github.com/WilliamSchweitzer/cartside/backend/internal/catalog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrUnauthenticated     = errors.New("verified principal required")
	ErrInvalidInput        = errors.New("invalid checkout input")
	ErrIdempotencyConflict = errors.New("checkout key already used with different input")
	ErrEmptyCart           = errors.New("cart is empty")
	ErrStaleVersion        = errors.New("cart version changed")
	ErrReviewedItems       = errors.New("reviewed items differ from cart")
	ErrUnavailable         = errors.New("product unavailable")
	ErrPriceChanged        = errors.New("reviewed price changed")
	ErrOverflow            = errors.New("order cents overflow")
	ErrStorage             = errors.New("checkout storage failure")
)

type ReviewedItem struct {
	ProductID      string
	Quantity       int
	UnitPriceCents int64
}
type CheckoutInput struct {
	IdempotencyKey      string
	ExpectedCartVersion int64
	Items               []ReviewedItem
}
type Item struct {
	ProductID      string
	Name           string
	PackageLabel   string
	Quantity       int
	UnitPriceCents int64
	LineTotalCents int64
}
type Order struct {
	ID           string
	Confirmation string
	Kind         string
	CreatedAt    time.Time
	Currency     string
	TotalCents   int64
	Items        []Item
}
type Service struct {
	pool     *pgxpool.Pool
	carts    *cart.Service
	products *catalog.Repository
}

func NewService(pool *pgxpool.Pool, carts *cart.Service, products *catalog.Repository) *Service {
	return &Service{pool, carts, products}
}

// ValidateInput applies the same authoritative validation used by Checkout.
func ValidateInput(in CheckoutInput) error {
	_, _, err := canonical(in)
	return err
}

func canonical(in CheckoutInput) (CheckoutInput, []byte, error) {
	if len(in.IdempotencyKey) < 1 || len(in.IdempotencyKey) > 128 || in.ExpectedCartVersion < 0 || len(in.Items) == 0 {
		return in, nil, ErrInvalidInput
	}
	// Opaque, case-sensitive ASCII key: reject whitespace rather than silently trim.
	for _, c := range in.IdempotencyKey {
		if c < 33 || c > 126 {
			return in, nil, ErrInvalidInput
		}
	}
	in.Items = append([]ReviewedItem(nil), in.Items...)
	for i := range in.Items {
		v := &in.Items[i]
		var id pgtype.UUID
		if id.Scan(v.ProductID) != nil || !id.Valid || v.Quantity < 1 || v.Quantity > 99 || v.UnitPriceCents < 0 {
			return in, nil, ErrInvalidInput
		}
		v.ProductID = id.String()
	}
	sort.Slice(in.Items, func(i, j int) bool { return in.Items[i].ProductID < in.Items[j].ProductID })
	for i := 1; i < len(in.Items); i++ {
		if in.Items[i].ProductID == in.Items[i-1].ProductID {
			return in, nil, ErrInvalidInput
		}
	}
	// Versioned canonical encoding; order of reviewed items and UUID casing are immaterial.
	b, _ := json.Marshal(struct {
		Schema  int
		Version int64
		Items   []ReviewedItem
	}{1, in.ExpectedCartVersion, in.Items})
	hash := sha256.Sum256(b)
	return in, hash[:], nil
}
func addTotal(total, price int64, quantity int) (int64, int64, error) {
	if price < 0 || quantity < 1 || price > math.MaxInt64/int64(quantity) {
		return 0, 0, ErrOverflow
	}
	line := price * int64(quantity)
	if total < 0 || line > math.MaxInt64-total {
		return 0, 0, ErrOverflow
	}
	return total + line, line, nil
}
func storage(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrStorage
}
func cleanup(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func (s *Service) Checkout(ctx context.Context, input CheckoutInput) (Order, error) {
	owner, ok := auth.FromContext(ctx)
	if !ok {
		return Order{}, ErrUnauthenticated
	}
	in, fp, err := canonical(input)
	if err != nil {
		return Order{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Order{}, storage(ctx, err)
	}
	defer cleanup(tx)
	if o, found, err := lookup(ctx, tx, owner, in.IdempotencyKey, fp); err != nil || found {
		return o, err
	}
	locked, err := s.carts.LockForCheckout(ctx, tx)
	if err != nil {
		return Order{}, storage(ctx, err)
	}
	// READ COMMITTED sees a preceding checkout after waiting for its cart lock.
	if o, found, err := lookup(ctx, tx, owner, in.IdempotencyKey, fp); err != nil || found {
		return o, err
	}
	if locked == nil || len(locked.Items) == 0 {
		return Order{}, ErrEmptyCart
	}
	if locked.Version != in.ExpectedCartVersion {
		return Order{}, ErrStaleVersion
	}
	if len(locked.Items) != len(in.Items) {
		return Order{}, ErrReviewedItems
	}
	o := Order{Currency: "USD", Kind: "simulated", Items: make([]Item, 0, len(in.Items))}
	for i, r := range in.Items {
		if locked.Items[i].ProductID != r.ProductID || locked.Items[i].Quantity != r.Quantity {
			return Order{}, ErrReviewedItems
		}
	}
	// Canonical product-ID order matches the cart's ordering and prevents lock cycles.
	for _, r := range in.Items {
		p, err := s.products.GetForShare(ctx, tx, r.ProductID)
		if errors.Is(err, catalog.ErrNotFound) {
			return Order{}, ErrUnavailable
		}
		if err != nil {
			return Order{}, storage(ctx, err)
		}
		if !p.Available {
			return Order{}, ErrUnavailable
		}
		if int64(p.PriceCents) != r.UnitPriceCents {
			return Order{}, ErrPriceChanged
		}
		total, line, err := addTotal(o.TotalCents, int64(p.PriceCents), r.Quantity)
		if err != nil {
			return Order{}, err
		}
		o.TotalCents = total
		o.Items = append(o.Items, Item{p.ID, p.Name, p.PackageLabel, r.Quantity, int64(p.PriceCents), line})
	}
	err = tx.QueryRow(ctx, `INSERT INTO orders(owner_issuer,owner_subject,currency,total_cents,idempotency_key,request_fingerprint) VALUES($1,$2,'USD',$3,$4,$5) RETURNING id::text,confirmation,created_at`, owner.Issuer, owner.Subject, o.TotalCents, in.IdempotencyKey, fp).Scan(&o.ID, &o.Confirmation, &o.CreatedAt)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			cleanup(tx)
			replay, e := s.pool.Begin(ctx)
			if e != nil {
				return Order{}, storage(ctx, e)
			}
			defer cleanup(replay)
			result, found, e := lookup(ctx, replay, owner, in.IdempotencyKey, fp)
			if e != nil || found {
				return result, e
			}
		}
		return Order{}, storage(ctx, err)
	}
	for _, i := range o.Items {
		if _, err = tx.Exec(ctx, `INSERT INTO order_items(order_id,product_id,name,package_label,quantity,unit_price_cents,line_total_cents) VALUES($1,$2,$3,$4,$5,$6,$7)`, o.ID, i.ProductID, i.Name, i.PackageLabel, i.Quantity, i.UnitPriceCents, i.LineTotalCents); err != nil {
			return Order{}, storage(ctx, err)
		}
	}
	if err = s.carts.ClearForCheckout(ctx, locked); err != nil {
		return Order{}, storage(ctx, err)
	}
	// A commit error may be ambiguous. Never retry it internally; callers reuse their key.
	if err = tx.Commit(ctx); err != nil {
		return Order{}, storage(ctx, err)
	}
	return o, nil
}
func lookup(ctx context.Context, tx pgx.Tx, owner auth.Principal, key string, fp []byte) (Order, bool, error) {
	var o Order
	var stored []byte
	err := tx.QueryRow(ctx, `SELECT id::text,confirmation,kind,created_at,currency,total_cents,request_fingerprint FROM orders WHERE owner_issuer=$1 AND owner_subject=$2 AND idempotency_key=$3`, owner.Issuer, owner.Subject, key).Scan(&o.ID, &o.Confirmation, &o.Kind, &o.CreatedAt, &o.Currency, &o.TotalCents, &stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, false, nil
	}
	if err != nil {
		return Order{}, false, storage(ctx, err)
	}
	if !bytes.Equal(fp, stored) {
		return Order{}, true, ErrIdempotencyConflict
	}
	rows, err := tx.Query(ctx, `SELECT product_id::text,name,package_label,quantity,unit_price_cents,line_total_cents FROM order_items WHERE order_id=$1 ORDER BY product_id`, o.ID)
	if err != nil {
		return Order{}, true, storage(ctx, err)
	}
	defer rows.Close()
	o.Items = make([]Item, 0)
	for rows.Next() {
		var i Item
		if err := rows.Scan(&i.ProductID, &i.Name, &i.PackageLabel, &i.Quantity, &i.UnitPriceCents, &i.LineTotalCents); err != nil {
			return Order{}, true, storage(ctx, err)
		}
		o.Items = append(o.Items, i)
	}
	if err := rows.Err(); err != nil {
		return Order{}, true, storage(ctx, err)
	}
	return o, true, nil
}
