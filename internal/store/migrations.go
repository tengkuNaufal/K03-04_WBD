package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"dbmp/internal/model"
)

const migCols = `m.id, m.database_id, m.name, m.sql_text, m.down_sql, m.irreversible_reason, m.mode,
	m.batch_table, m.batch_column, m.batch_size, m.status, m.version, m.created_by, m.created_at,
	EXISTS (SELECT 1 FROM executions e WHERE e.migration_id = m.id)`

func scanMigration(row pgx.Row) (model.Migration, error) {
	var m model.Migration
	err := row.Scan(&m.ID, &m.DatabaseID, &m.Name, &m.UpSQL, &m.DownSQL, &m.IrreversibleReason, &m.Mode,
		&m.BatchTable, &m.BatchColumn, &m.BatchSize, &m.Status, &m.Version, &m.CreatedBy, &m.CreatedAt, &m.Locked)
	return m, mapErr(err)
}

func (s *Store) CreateMigration(ctx context.Context, m model.Migration) (int64, error) {
	return s.insert(ctx, `INSERT INTO migrations (database_id, name, sql_text, down_sql, irreversible_reason, mode,
		batch_table, batch_column, batch_size, created_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		m.DatabaseID, m.Name, m.UpSQL, m.DownSQL, m.IrreversibleReason, m.Mode, m.BatchTable, m.BatchColumn, m.BatchSize, m.CreatedBy)
}

func (s *Store) GetMigration(ctx context.Context, databaseID, id int64) (model.Migration, error) {
	return scanMigration(s.pool.QueryRow(ctx, `SELECT `+migCols+` FROM migrations m WHERE m.id = $1 AND m.database_id = $2`, id, databaseID))
}

func (s *Store) ListMigrations(ctx context.Context, databaseID int64) ([]model.Migration, error) {
	rows, _ := s.pool.Query(ctx, `SELECT `+migCols+` FROM migrations m WHERE m.database_id = $1
		ORDER BY m.version NULLS LAST, m.created_at, m.id`, databaseID)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Migration, error) { return scanMigration(r) })
}

const editable = `status = 'draft' AND NOT EXISTS (SELECT 1 FROM executions e WHERE e.migration_id = migrations.id)
	AND NOT EXISTS (SELECT 1 FROM change_requests c WHERE c.migration_id = migrations.id
		AND c.status IN ('pending_review', 'approved', 'queued', 'running'))`

func (s *Store) UpdateMigration(ctx context.Context, m model.Migration) error {
	return s.exec(ctx, `UPDATE migrations SET name = $3, sql_text = $4, down_sql = $5, irreversible_reason = $6,
		mode = $7, batch_table = $8, batch_column = $9, batch_size = $10
		WHERE id = $1 AND database_id = $2 AND `+editable,
		m.ID, m.DatabaseID, m.Name, m.UpSQL, m.DownSQL, m.IrreversibleReason, m.Mode, m.BatchTable, m.BatchColumn, m.BatchSize)
}

func (s *Store) DeleteMigration(ctx context.Context, databaseID, id int64) error {
	return s.exec(ctx, `DELETE FROM migrations WHERE id = $1 AND database_id = $2 AND `+editable, id, databaseID)
}
