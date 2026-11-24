package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"dbmp/internal/targetdb"
)

func TestTransient(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&targetdb.ConnError{Err: errors.New("connection refused")}, true},
		{context.Canceled, true},
		{&pgconn.PgError{Code: "40001"}, true},
		{&pgconn.PgError{Code: "40P01"}, true}, 
		{&pgconn.PgError{Code: "08006"}, true},
		{fmt.Errorf("batch 3: %w", &pgconn.PgError{Code: "55P03"}), true},
		{&pgconn.PgError{Code: "23514"}, false}, 
		{&pgconn.PgError{Code: "42P01"}, false},
		{errors.New("other"), false},
	}
	for _, c := range cases {
		if got := Transient(c.err); got != c.want {
			t.Errorf("Transient(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestBackoffIsBounded(t *testing.T) {
	if backoff(1) != 2*time.Second || backoff(2) != 4*time.Second || backoff(3) != 8*time.Second {
		t.Fatal("backoff should double")
	}
	if backoff(50) != time.Minute {
		t.Fatalf("backoff(50) = %v", backoff(50))
	}
}
