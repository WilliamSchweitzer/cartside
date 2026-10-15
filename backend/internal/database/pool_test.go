package database

import (
	"context"
	"strings"
	"testing"
)

func TestInvalidConfiguration(t *testing.T) {
	for _, value := range []string{"", " ", "not-a-url", "https://user:secret@localhost/db", "postgres://localhost/db", "postgres://user:secret@localhost", "postgres://user:secret@localhost/db?pool_max_conns=0"} {
		pool, err := NewPool(context.Background(), value)
		if pool != nil {
			pool.Close()
		}
		if err == nil {
			t.Fatal("expected invalid configuration to fail")
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("error leaked credentials")
		}
	}
}

func TestPoolDoesNotRequireReachableDatabase(t *testing.T) {
	pool, err := NewPool(context.Background(), "postgres://user:secret@127.0.0.1:1/db?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
}
