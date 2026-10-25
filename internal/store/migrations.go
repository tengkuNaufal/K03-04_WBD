package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"dbmp/internal/model"
)

const migCols = `id, database_id, name, sql_text, status, version, created_by, created_at,
	COALESCE(executed_by, ''), executed_at, COALESCE(error, '')`

func scanMigration(row pgx.Row) (model.Migration, error) {
	var m model.Migration
	err := row.Scan(&m.ID, &m.DatabaseID, &m.Name, &m.SQL, &m.Status, &m.Version, &m.CreatedBy, &m.CreatedAt,
		&m.ExecutedBy, &m.ExecutedAt, &m.Error)
	return m, mapErr(err)
}

func (s *Store) CreateMigration(ctx context.Context, databaseID int64, name, sql, username string) (int64, error) {
	return s.insert(ctx, `INSERT INTO migrations (database_id, name, sql_text, created_by) VALUES ($1, $2, $3, $4) RETURNING id`,
		databaseID, name, sql, username)
}

func (s *Store) GetMigration(ctx context.Context, databaseID, id int64) (model.Migration, error) {
	return scanMigration(s.pool.QueryRow(ctx, `SELECT `+migCols+` FROM migrations WHERE id = $1 AND database_id = $2`, id, databaseID))
}

func (s *Store) ListMigrations(ctx context.Context, databaseID int64) ([]model.Migration, error) {
	rows, _ := s.pool.Query(ctx, `SELECT `+migCols+` FROM migrations WHERE database_id = $1
		ORDER BY version NULLS LAST, created_at, id`, databaseID)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Migration, error) { return scanMigration(r) })
}

func (s *Store) UpdateMigration(ctx context.Context, databaseID, id int64, name, sql string) error {
	return s.exec(ctx, `UPDATE migrations SET name = $3, sql_text = $4
		WHERE id = $1 AND database_id = $2 AND status <> 'applied'`, id, databaseID, name, sql)
}

func (s *Store) DeleteMigration(ctx context.Context, databaseID, id int64) error {
	return s.exec(ctx, `DELETE FROM migrations WHERE id = $1 AND database_id = $2 AND status <> 'applied'`, id, databaseID)
}

func (s *Store) RunMigration(ctx context.Context, databaseID, id int64, username string,
	run func(sql string) (failure string, err error)) (model.Migration, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return model.Migration{}, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT 1 FROM databases WHERE id = $1 FOR UPDATE`, databaseID); err != nil {
		return model.Migration{}, err
	}
	m, err := scanMigration(tx.QueryRow(ctx, `SELECT `+migCols+` FROM migrations WHERE id = $1 AND database_id = $2`, id, databaseID))
	if err != nil {
		return m, err
	}
	if m.Status == model.MigrationApplied {
		return m, model.Invalid("Migration ini sudah diterapkan.")
	}

	failure, err := run(m.SQL)
	if err != nil {
		return m, err
	}
	if failure == "" {
		_, err = tx.Exec(ctx, `UPDATE migrations SET status = 'applied', executed_by = $2, executed_at = $3, error = NULL,
			version = (SELECT COALESCE(MAX(version), 0) + 1 FROM migrations WHERE database_id = $4) WHERE id = $1`,
			id, username, time.Now(), databaseID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE migrations SET status = 'failed', executed_by = $2, executed_at = $3, error = $4 WHERE id = $1`,
			id, username, time.Now(), failure)
	}
	if err != nil {
		return m, err
	}
	if err := tx.Commit(ctx); err != nil {
		return m, err
	}
	return s.GetMigration(ctx, databaseID, id)
}
