package main

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/database"
	"github.com/WilliamSchweitzer/cartside/backend/migrations"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	if len(os.Args) != 2 || (os.Args[1] != "up" && os.Args[1] != "down") {
		return errors.New("usage: migrate up|down (down deletes catalog data)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := database.NewPool(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := migrations.NewProvider(db)
	if err != nil {
		return errors.New("migration configuration failed")
	}
	var results []*goose.MigrationResult
	if os.Args[1] == "up" {
		results, err = provider.Up(ctx)
	} else {
		var result *goose.MigrationResult
		result, err = provider.Down(ctx)
		if result != nil {
			results = append(results, result)
		}
	}
	if err != nil {
		return errors.New("migration failed; check database availability and schema state")
	}
	log.Printf("migrations applied: %d", len(results))
	return nil
}
