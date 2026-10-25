package targetdb

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"dbmp/internal/model"
)

func (m *Manager) Exec(ctx context.Context, id int64, script string) (failure string, err error) {
	ctx, cancel := context.WithTimeout(ctx, ExecTimeout)
	defer cancel()
	tx, done, err := m.begin(ctx, id, pgx.TxOptions{}, ExecTimeout)
	if err != nil {
		return "", err
	}
	defer done()
	if _, err := tx.Exec(ctx, script); err != nil {
		return PgErrorMessage(err), nil
	}
	if err := tx.Commit(ctx); err != nil {
		return "commit gagal: " + PgErrorMessage(err), nil
	}
	return "", nil
}

func (m *Manager) Explain(ctx context.Context, id int64, stmts []Statement, analyze bool) ([]model.ExplainResult, error) {
	ctx, cancel := context.WithTimeout(ctx, ExecTimeout)
	defer cancel()
	tx, done, err := m.begin(ctx, id, pgx.TxOptions{}, ExecTimeout)
	if err != nil {
		return nil, err
	}
	defer done()

	opts := "VERBOSE, COSTS"
	if analyze {
		opts = "ANALYZE, BUFFERS, " + opts
	}
	var out []model.ExplainResult
	failed := false
	for _, st := range stmts {
		r := model.ExplainResult{Statement: st.Text}
		switch {
		case failed:
			r.Note = "Dilewati karena statement sebelumnya gagal."
		case !st.Explainable():
			if _, err := tx.Exec(ctx, st.Text); err != nil {
				r.Error, failed = PgErrorMessage(err), true
			} else {
				r.Note = st.Keyword + " tidak dapat di-EXPLAIN; statement dijalankan dalam transaksi uji agar statement berikutnya melihat efeknya."
			}
		default:
			if r.Plan, err = explainOne(ctx, tx, opts, st.Text); err != nil {
				r.Error, failed = PgErrorMessage(err), true
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func explainOne(ctx context.Context, tx pgx.Tx, opts, stmt string) (string, error) {
	rows, _ := tx.Query(ctx, "EXPLAIN ("+opts+") "+stmt, pgx.QueryExecModeSimpleProtocol)
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	return strings.Join(lines, "\n"), err
}
