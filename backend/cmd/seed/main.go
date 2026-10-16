package main

import (
	"context"
	"errors"
	"log"
	"os"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/catalogseed"
	"github.com/WilliamSchweitzer/cartside/backend/internal/database"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	if len(os.Args) != 1 {
		return errors.New("usage: seed (development sample data only)")
	}
	products, err := catalogseed.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := database.NewPool(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	result, err := catalogseed.Seed(ctx, pool, products)
	if err != nil {
		return err
	}
	log.Printf("development products: inserted=%d skipped=%d", result.Inserted, result.Skipped)
	return nil
}
