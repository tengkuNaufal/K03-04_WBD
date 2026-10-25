package store

import (
	"context"
	_ "embed"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"dbmp/internal/model"
)

var schema string

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, schema)
	return err
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func mapErr(err error) error {
	switch {
	case errors.Is(err, pgx.ErrNoRows), pgCode(err) == "23503": 
		return model.ErrNotFound
	case pgCode(err) == "23505": 
		return model.ErrConflict
	}
	return err
}

func (s *Store) exec(ctx context.Context, sql string, args ...any) error {
	tag, err := s.pool.Exec(ctx, sql, args...)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return model.ErrNotFound
	}
	return nil
}

func (s *Store) insert(ctx context.Context, sql string, args ...any) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, sql, args...).Scan(&id)
	return id, mapErr(err)
}
