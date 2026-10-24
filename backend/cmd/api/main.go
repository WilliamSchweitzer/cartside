package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/auth"
	"github.com/WilliamSchweitzer/cartside/backend/internal/cart"
	"github.com/WilliamSchweitzer/cartside/backend/internal/catalog"
	"github.com/WilliamSchweitzer/cartside/backend/internal/database"
	"github.com/WilliamSchweitzer/cartside/backend/internal/httpapi"
	"github.com/WilliamSchweitzer/cartside/backend/internal/orders"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		slog.Error("process_failed", "error_category", "startup_or_server_failure")
		os.Exit(1)
	}
}

func run() error {
	csrf, err := httpapi.NewCSRF(os.Getenv("BROWSER_ORIGIN"))
	if err != nil {
		return err
	}
	verifier, err := auth.New(os.Getenv("OIDC_ISSUER"), os.Getenv("OIDC_AUDIENCE"), os.Getenv("OIDC_JWKS_URL"))
	if err != nil {
		return err
	}
	pool, err := database.NewPool(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()

	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	products := catalog.NewRepository(pool)
	carts := cart.NewService(pool, products)
	assistant, limiter, err := httpapi.ConfigureAssistant(os.Getenv, products)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           httpapi.NewRouterWithAssistant(pool.Ping, verifier.Middleware, products, csrf, carts, orders.NewService(pool, carts, products), assistant, limiter),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("server_starting", "component", "api")
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		stop() // Restore default signal behavior so a second signal can force exit.
	}

	slog.Info("server_stopping", "component", "api")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return err
	}
	if err := <-serverErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	slog.Info("server_stopped", "component", "api")
	return nil
}
