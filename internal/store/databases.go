package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"dbmp/internal/model"
)

const dbCols = `d.id, d.name, d.host, d.port, d.dbname, d.username, d.password_enc, d.sslmode`

func scanDatabase(row pgx.Row) (model.Database, error) {
	var d model.Database
	err := row.Scan(&d.ID, &d.Name, &d.Host, &d.Port, &d.DBName, &d.Username, &d.PasswordEnc, &d.SSLMode)
	return d, mapErr(err)
}

func (s *Store) CreateDatabase(ctx context.Context, d model.Database) (int64, error) {
	return s.insert(ctx, `
		INSERT INTO databases (name, host, port, dbname, username, password_enc, sslmode)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		d.Name, d.Host, d.Port, d.DBName, d.Username, d.PasswordEnc, d.SSLMode)
}

// UpdateDatabase keeps the stored password when PasswordEnc is nil.
func (s *Store) UpdateDatabase(ctx context.Context, d model.Database) error {
	return s.exec(ctx, `
		UPDATE databases SET name = $2, host = $3, port = $4, dbname = $5, username = $6,
		       password_enc = COALESCE($7, password_enc), sslmode = $8
		WHERE id = $1`,
		d.ID, d.Name, d.Host, d.Port, d.DBName, d.Username, d.PasswordEnc, d.SSLMode)
}

func (s *Store) DeleteDatabase(ctx context.Context, id int64) error {
	return s.exec(ctx, `DELETE FROM databases WHERE id = $1`, id)
}

func (s *Store) GetDatabase(ctx context.Context, id int64) (model.Database, error) {
	return scanDatabase(s.pool.QueryRow(ctx, `SELECT `+dbCols+` FROM databases d WHERE d.id = $1`, id))
}

func (s *Store) ListDatabases(ctx context.Context) ([]model.Database, error) {
	rows, _ := s.pool.Query(ctx, `SELECT `+dbCols+` FROM databases d ORDER BY d.name`)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Database, error) { return scanDatabase(r) })
}
