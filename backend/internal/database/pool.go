package database

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool validates configuration and creates a shared pool without requiring
// PostgreSQL to be reachable. The caller owns closing the pool.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	invalid := errors.New("DATABASE_URL must be a valid PostgreSQL connection URL")
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	u, err := url.Parse(databaseURL)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || u.User == nil || u.User.Username() == "" || strings.Trim(u.Path, "/") == "" {
		return nil, invalid
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, invalid // Parser errors can include credentials.
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, invalid
	}
	return pool, nil
}
