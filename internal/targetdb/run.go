package targetdb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const MigrationTimeout = time.Hour

const checkpointDDL = `CREATE TABLE IF NOT EXISTS public.dbmp_checkpoints (
	series_id     BIGINT NOT NULL,
	batch         INT NOT NULL,
	direction     TEXT NOT NULL,
	lo            BIGINT,
	hi            BIGINT,
	rows_affected BIGINT NOT NULL,
	execution_id  BIGINT NOT NULL,
	committed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (series_id, batch))`

type Checkpoint struct {
	Batch  int
	Lo, Hi *int64
	Rows   int64
}

var ErrAlreadyCommitted = errors.New("checkpoint already committed")

type UncertainError struct{ Err error }

func (e *UncertainError) Error() string { return "hasil COMMIT tidak pasti: " + e.Err.Error() }
func (e *UncertainError) Unwrap() error { return e.Err }

func (m *Manager) EnsureCheckpoints(ctx context.Context, id int64) error {
	p, err := m.pool(ctx, id)
	if err != nil {
		return err
	}
	if _, err := p.Exec(ctx, checkpointDDL); err != nil {
		return connOr(err)
	}
	return nil
}

func (m *Manager) Checkpoints(ctx context.Context, id, series int64) ([]Checkpoint, error) {
	p, err := m.pool(ctx, id)
	if err != nil {
		return nil, err
	}
	rows, _ := p.Query(ctx, `SELECT batch, lo, hi, rows_affected FROM public.dbmp_checkpoints WHERE series_id = $1 ORDER BY batch`, series)
	cps, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Checkpoint])
	return cps, connOr(err)
}

func (m *Manager) step(ctx context.Context, id int64, cp Checkpoint, series, execID int64, dir string,
	fn func(pgx.Tx) (int64, error)) (int64, error) {
	tx, done, err := m.begin(ctx, id, pgx.TxOptions{}, MigrationTimeout)
	if err != nil {
		return 0, err
	}
	defer done()
	n, err := fn(tx)
	if err != nil {
		return 0, connOr(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO public.dbmp_checkpoints (series_id, batch, direction, lo, hi, rows_affected, execution_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, series, cp.Batch, dir, cp.Lo, cp.Hi, n, execID)
	if pgCode(err) == "23505" {
		return 0, ErrAlreadyCommitted
	}
	if err != nil {
		return 0, connOr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, &UncertainError{Err: err}
	}
	return n, nil
}

func (m *Manager) RunOnce(ctx context.Context, id, series, execID int64, dir, script string) (int64, error) {
	return m.step(ctx, id, Checkpoint{Batch: 0}, series, execID, dir, func(tx pgx.Tx) (int64, error) {
		tag, err := tx.Exec(ctx, script, pgx.QueryExecModeSimpleProtocol)
		return tag.RowsAffected(), err
	})
}

func (m *Manager) RunBatch(ctx context.Context, id, series, execID int64, dir string, batch int, sql string, lo, hi int64) (int64, error) {
	return m.step(ctx, id, Checkpoint{Batch: batch, Lo: &lo, Hi: &hi}, series, execID, dir, func(tx pgx.Tx) (int64, error) {
		tag, err := tx.Exec(ctx, sql, lo, hi)
		return tag.RowsAffected(), err
	})
}

func quoteIdent(name string) string {
	return pgx.Identifier(strings.Split(name, ".")).Sanitize()
}

func (m *Manager) ScopeMax(ctx context.Context, id int64, table, col string) (*int64, error) {
	p, err := m.pool(ctx, id)
	if err != nil {
		return nil, err
	}
	var v *int64
	err = p.QueryRow(ctx, fmt.Sprintf("SELECT max(%s)::bigint FROM %s", quoteIdent(col), quoteIdent(table))).Scan(&v)
	return v, connOr(err)
}

func (m *Manager) NextHi(ctx context.Context, id int64, table, col string, lo, scopeMax int64, size int) (int64, bool, error) {
	p, err := m.pool(ctx, id)
	if err != nil {
		return 0, false, err
	}
	c := quoteIdent(col)
	var hi *int64
	err = p.QueryRow(ctx, fmt.Sprintf(`SELECT max(k) FROM (SELECT %s::bigint AS k FROM %s WHERE %s > $1 AND %s <= $2 ORDER BY %s LIMIT $3) s`,
		c, quoteIdent(table), c, c, c), lo, scopeMax, size).Scan(&hi)
	if err != nil {
		return 0, false, connOr(err)
	}
	if hi == nil {
		return 0, false, nil
	}
	return *hi, true, nil
}

func connOr(err error) error {
	if err == nil || pgCode(err) != "" || errors.Is(err, ErrAlreadyCommitted) {
		return err
	}
	var ce *ConnError
	if errors.As(err, &ce) || errors.Is(err, context.Canceled) {
		return err
	}
	return &ConnError{Err: err}
}
