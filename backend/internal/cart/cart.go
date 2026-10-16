// Package cart owns persistent carts and their contents. Callers must supply a
// context populated by auth.Verifier; no operation accepts an owner or cart ID.
package cart

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/auth"
	"github.com/WilliamSchweitzer/cartside/backend/internal/catalog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrUnauthenticated  = errors.New("verified principal required")
	ErrInvalidProductID = errors.New("invalid product ID")
	ErrQuantity         = errors.New("quantity must be between 1 and 99")
	ErrUnavailable      = errors.New("product unavailable for addition or increase")
	ErrItemNotFound     = errors.New("cart item not found")
	ErrTotalOverflow    = errors.New("cart subtotal exceeds integer cents range")
)

// Cart uses current prices, including unavailable items. Its subtotal is a
// display value, not an inventory reservation or checkout price guarantee.
type Cart struct {
	Version       int64
	Items         []Item
	SubtotalCents int64
	Currency      string
}

type Item struct {
	Product        catalog.Product
	Quantity       int
	LineTotalCents int64
}

type Service struct {
	pool     *pgxpool.Pool
	products *catalog.Repository
}

func NewService(pool *pgxpool.Pool, products *catalog.Repository) *Service {
	return &Service{pool: pool, products: products}
}

// Get returns an empty version-zero representation without creating a cart.
// Repeatable read keeps contents, version and catalog prices in one snapshot.
func (s *Service) Get(ctx context.Context) (Cart, error) {
	owner, ok := auth.FromContext(ctx)
	if !ok {
		return Cart{}, ErrUnauthenticated
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Cart{}, fmt.Errorf("read cart: %w", err)
	}
	defer rollback(tx)
	result := Cart{Items: make([]Item, 0), Currency: "USD"}
	var id string
	err = tx.QueryRow(ctx, `SELECT id::text,version FROM carts WHERE owner_issuer=$1 AND owner_subject=$2`, owner.Issuer, owner.Subject).Scan(&id, &result.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return Cart{}, fmt.Errorf("read cart: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT product_id::text,quantity FROM cart_items WHERE cart_id=$1 ORDER BY product_id`, id)
	if err != nil {
		return Cart{}, fmt.Errorf("read items: %w", err)
	}
	// Close the rows before catalog operations use this same transaction connection.
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.Product.ID, &item.Quantity); err != nil {
			rows.Close()
			return Cart{}, err
		}
		result.Items = append(result.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Cart{}, fmt.Errorf("read items: %w", err)
	}
	for i := range result.Items {
		result.Items[i].Product, err = s.products.GetInTx(ctx, tx, result.Items[i].Product.ID)
		if err != nil {
			return Cart{}, fmt.Errorf("read cart product: %w", err)
		}
	}
	if err := calculateTotals(&result); err != nil {
		return Cart{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Cart{}, fmt.Errorf("read cart: %w", err)
	}
	return result, nil
}

// Add increments quantity and is not idempotent. It never retries automatically.
// A failed first addition also rolls back creation of the cart row.
func (s *Service) Add(ctx context.Context, productID string, quantity int) error {
	return s.mutate(ctx, productID, quantity, add)
}

// SetQuantity requires an existing item. Setting its current quantity is a no-op.
func (s *Service) SetQuantity(ctx context.Context, productID string, quantity int) error {
	return s.mutate(ctx, productID, quantity, set)
}

// Remove succeeds even if the cart or item does not exist, without creating one.
func (s *Service) Remove(ctx context.Context, productID string) error {
	return s.mutate(ctx, productID, 0, remove)
}

type mutation int

const (
	add mutation = iota
	set
	remove
)

func (s *Service) mutate(ctx context.Context, productID string, quantity int, op mutation) error {
	owner, ok := auth.FromContext(ctx)
	if !ok {
		return ErrUnauthenticated
	}
	var productUUID pgtype.UUID
	if err := productUUID.Scan(productID); err != nil || !productUUID.Valid {
		return ErrInvalidProductID
	}
	if op != remove && (quantity < 1 || quantity > 99) {
		return ErrQuantity
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("mutate cart: %w", err)
	}
	defer rollback(tx)
	if op == add {
		// Concurrent first additions converge on the database's unique identity pair.
		_, err = tx.Exec(ctx, `INSERT INTO carts (owner_issuer,owner_subject) VALUES ($1,$2) ON CONFLICT (owner_issuer,owner_subject) DO NOTHING`, owner.Issuer, owner.Subject)
		if err != nil {
			return fmt.Errorf("create cart: %w", err)
		}
	}
	var cartID string
	// Lock order for every mutation (and future checkout): cart, then products.
	err = tx.QueryRow(ctx, `SELECT id::text FROM carts WHERE owner_issuer=$1 AND owner_subject=$2 FOR UPDATE`, owner.Issuer, owner.Subject).Scan(&cartID)
	if errors.Is(err, pgx.ErrNoRows) {
		if op == remove {
			return nil
		}
		return ErrItemNotFound
	}
	if err != nil {
		return fmt.Errorf("lock cart: %w", err)
	}
	current := 0
	err = tx.QueryRow(ctx, `SELECT quantity FROM cart_items WHERE cart_id=$1 AND product_id=$2`, cartID, productUUID).Scan(&current)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read quantity: %w", err)
	}
	if op == set && current == 0 {
		return ErrItemNotFound
	}
	next, err := nextQuantity(current, quantity, op)
	if err != nil {
		return err
	}
	if next == current {
		return nil
	}
	if next > current {
		product, err := s.products.GetForShare(ctx, tx, productID)
		if err != nil {
			return fmt.Errorf("check product: %w", err)
		}
		if err := checkAvailability(current, next, product.Available); err != nil {
			return err
		}
	}
	if next == 0 {
		_, err = tx.Exec(ctx, `DELETE FROM cart_items WHERE cart_id=$1 AND product_id=$2`, cartID, productUUID)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO cart_items (cart_id,product_id,quantity) VALUES ($1,$2,$3) ON CONFLICT (cart_id,product_id) DO UPDATE SET quantity=EXCLUDED.quantity`, cartID, productUUID, next)
	}
	if err != nil {
		return fmt.Errorf("write item: %w", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE carts SET version=version+1 WHERE id=$1`, cartID); err != nil {
		return fmt.Errorf("advance cart version: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit cart: %w", err)
	}
	return nil
}

func nextQuantity(current, requested int, op mutation) (int, error) {
	if op == remove {
		return 0, nil
	}
	if requested < 1 || requested > 99 {
		return 0, ErrQuantity
	}
	if op == add {
		// Check before adding so even extreme input cannot wrap an integer.
		if requested > 99-current {
			return 0, ErrQuantity
		}
		return current + requested, nil
	}
	return requested, nil
}

func checkAvailability(current, next int, available bool) error {
	if next > current && !available {
		return ErrUnavailable
	}
	return nil
}

func calculateTotals(result *Cart) error {
	result.SubtotalCents = 0
	for i := range result.Items {
		item := &result.Items[i]
		item.LineTotalCents = int64(item.Product.PriceCents) * int64(item.Quantity)
		if item.LineTotalCents > math.MaxInt64-result.SubtotalCents {
			return ErrTotalOverflow
		}
		result.SubtotalCents += item.LineTotalCents
	}
	return nil
}

func rollback(tx pgx.Tx) {
	// Cleanup is bounded independently so an expired operation context releases locks.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
