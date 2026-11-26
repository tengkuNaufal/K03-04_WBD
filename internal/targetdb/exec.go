package targetdb

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"dbmp/internal/model"
)

func (m *Manager) Explain(ctx context.Context, id int64, stmts []Statement, analyze bool) ([]model.ExplainResult, error) {
	ctx, cancel := context.WithTimeout(ctx, ExecTimeout)
	defer cancel()
	tx, done, err := m.begin(ctx, id, pgx.TxOptions{}, ExecTimeout)
	if err != nil {
		return nil, err
	}
	defer done()

	var out []model.ExplainResult
	failed := false
	for _, st := range stmts {
		r := model.ExplainResult{Statement: st.Text}
		params := strings.Contains(st.Text, "$1")
		switch {
		case failed:
			r.Note = "Dilewati karena statement sebelumnya gagal."
		case !st.Explainable():
			if _, err := tx.Exec(ctx, st.Text, pgx.QueryExecModeSimpleProtocol); err != nil {
				r.Error, failed = PgErrorMessage(err), true
			} else {
				r.Note = st.Keyword + " tidak dapat di-EXPLAIN; statement dijalankan dalam transaksi uji (di-rollback) agar statement berikutnya melihat efeknya."
			}
		case params && analyze:
			r.Note = "Statement batch berparameter tidak dapat di-EXPLAIN ANALYZE; gunakan EXPLAIN biasa (generic plan)."
		default:
			opts := "VERBOSE, COSTS"
			if analyze {
				opts = "ANALYZE, BUFFERS, " + opts
			} else if params {
				opts += ", GENERIC_PLAN"
			}
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
