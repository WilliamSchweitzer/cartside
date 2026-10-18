// mock-ai is local infrastructure, not an application/business module.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/WilliamSchweitzer/cartside/backend/internal/mockai"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	probe := flag.String("probe", "", "probe a local health/stats URL and exit")
	flag.Parse()
	if *probe != "" {
		c := http.Client{Timeout: 2 * time.Second}
		res, err := c.Get(*probe)
		if err != nil {
			return errors.New("probe failed")
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return errors.New("probe failed")
		}
		_, err = io.Copy(os.Stdout, io.LimitReader(res.Body, mockai.MaxResponseBytes))
		return err
	}
	h, err := mockai.New(os.Getenv("MOCK_TEST_MODE") == "1", os.Getenv("MOCK_SCENARIO"))
	if err != nil {
		return err
	}
	s := &http.Server{Addr: ":8090", Handler: h, ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 4096}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result := make(chan error, 1)
	go func() { result <- s.ListenAndServe() }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
	}
	deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(deadline); err != nil {
		return fmt.Errorf("mock shutdown: %w", err)
	}
	if err := <-result; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
