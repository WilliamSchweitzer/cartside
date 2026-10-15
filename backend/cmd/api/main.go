package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/database"
	"github.com/WilliamSchweitzer/cartside/backend/internal/httpapi"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
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

	server := &http.Server{
		Addr:              addr,
		Handler:           httpapi.NewRouter(pool.Ping),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("starting HTTP server on %s", addr)
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		stop() // Restore default signal behavior so a second signal can force exit.
	}

	log.Print("shutting down HTTP server")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return err
	}
	if err := <-serverErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Print("HTTP server stopped")
	return nil
}
