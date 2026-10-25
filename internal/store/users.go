package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"dbmp/internal/model"
)

const userCols = `id, username, password_hash, is_admin, is_active, created_at`

func scanUser(row pgx.Row) (model.User, error) {
	var u model.User
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.IsAdmin, &u.IsActive, &u.CreatedAt)
	return u, mapErr(err)
}

func (s *Store) CreateUser(ctx context.Context, username, passwordHash string, isAdmin bool) (int64, error) {
	return s.insert(ctx, `INSERT INTO users (username, password_hash, is_admin) VALUES ($1, $2, $3) RETURNING id`,
		username, passwordHash, isAdmin)
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (model.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE username = $1`, username))
}

func (s *Store) GetUserByID(ctx context.Context, id int64) (model.User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
}

func (s *Store) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, _ := s.pool.Query(ctx, `SELECT `+userCols+` FROM users ORDER BY username`)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.User, error) { return scanUser(r) })
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) UpdateUserFlags(ctx context.Context, id int64, isAdmin, isActive bool) error {
	return s.exec(ctx, `UPDATE users SET is_admin = $2, is_active = $3 WHERE id = $1`, id, isAdmin, isActive)
}

func (s *Store) SetPassword(ctx context.Context, id int64, hash string) error {
	return s.exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, id, hash)
}

func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	return s.exec(ctx, `DELETE FROM users WHERE id = $1`, id)
}
