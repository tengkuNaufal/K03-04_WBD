package targetdb

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"dbmp/internal/model"
)

const userRelations = `c.relkind IN ('r', 'p', 'v', 'm', 'f')
	AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_%'`

var relKinds = map[string]string{"r": "table", "p": "partitioned table", "v": "view", "m": "materialized view", "f": "foreign table"}

var constraintTypes = map[string]string{"p": "PRIMARY KEY", "u": "UNIQUE", "f": "FOREIGN KEY", "c": "CHECK", "x": "EXCLUDE", "n": "NOT NULL"}

func (m *Manager) readOnly(ctx context.Context, id int64, fn func(tx pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	tx, done, err := m.begin(ctx, id, pgx.TxOptions{AccessMode: pgx.ReadOnly}, QueryTimeout)
	if err != nil {
		return err
	}
	defer done()
	return fn(tx)
}

func (m *Manager) ListTables(ctx context.Context, id int64) ([]model.TableInfo, error) {
	var out []model.TableInfo
	err := m.readOnly(ctx, id, func(tx pgx.Tx) error {
		rows, _ := tx.Query(ctx, `
			SELECT n.nspname, c.relname, c.relkind::text, GREATEST(c.reltuples, 0)::bigint
			FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE `+userRelations+` ORDER BY 1, 2`)
		var err error
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.TableInfo, error) {
			var t model.TableInfo
			err := r.Scan(&t.Schema, &t.Name, &t.Kind, &t.EstimatedRows)
			t.Kind = relKinds[t.Kind]
			return t, err
		})
		return err
	})
	return out, err
}

// Table returns the columns, constraints and indexes of one relation.
func (m *Manager) Table(ctx context.Context, id int64, schema, table string) (model.TableDetail, error) {
	d := model.TableDetail{TableInfo: model.TableInfo{Schema: schema, Name: table}}
	err := m.readOnly(ctx, id, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT c.relkind::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relname = $2 AND `+userRelations, schema, table).Scan(&d.Kind); err != nil {
			if err == pgx.ErrNoRows {
				return model.ErrNotFound
			}
			return err
		}
		d.Kind = relKinds[d.Kind]
		const rel = `(SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relname = $2)`

		rows, _ := tx.Query(ctx, `
			SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull, COALESCE(pg_get_expr(ad.adbin, ad.adrelid), '')
			FROM pg_attribute a LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
			WHERE a.attrelid = `+rel+` AND a.attnum > 0 AND NOT a.attisdropped ORDER BY a.attnum`, schema, table)
		var err error
		if d.Columns, err = pgx.CollectRows(rows, pgx.RowToStructByPos[model.ColumnInfo]); err != nil {
			return err
		}

		rows, _ = tx.Query(ctx, `
			SELECT conname, contype::text, pg_get_constraintdef(oid, true) FROM pg_constraint
			WHERE conrelid = `+rel+` ORDER BY contype <> 'p', contype <> 'u', conname`, schema, table)
		if d.Constraints, err = pgx.CollectRows(rows, pgx.RowToStructByPos[model.ConstraintInfo]); err != nil {
			return err
		}
		for i := range d.Constraints {
			d.Constraints[i].Type = constraintTypes[d.Constraints[i].Type]
		}

		rows, _ = tx.Query(ctx, `
			SELECT i.relname, pg_get_indexdef(ix.indexrelid) FROM pg_index ix JOIN pg_class i ON i.oid = ix.indexrelid
			WHERE ix.indrelid = `+rel+` ORDER BY ix.indisprimary DESC, i.relname`, schema, table)
		d.Indexes, err = pgx.CollectRows(rows, pgx.RowToStructByPos[model.IndexInfo])
		return err
	})
	return d, err
}

func (m *Manager) Rows(ctx context.Context, id int64, schema, table string, limit, offset int) (model.RowPage, error) {
	page := model.RowPage{Offset: offset, Limit: limit}
	err := m.readOnly(ctx, id, func(tx pgx.Tx) error {
		// The identifier is quoted by pgx; limit and offset are integers.
		q := fmt.Sprintf("SELECT * FROM %s LIMIT %d OFFSET %d", pgx.Identifier{schema, table}.Sanitize(), limit+1, offset)
		rows, err := tx.Query(ctx, q, pgx.QueryExecModeSimpleProtocol)
		if err != nil {
			return err
		}
		defer rows.Close()
		for _, fd := range rows.FieldDescriptions() {
			page.Columns = append(page.Columns, fd.Name)
		}
		for rows.Next() {
			vals := make([]*string, len(rows.RawValues()))
			for i, b := range rows.RawValues() {
				if b != nil {
					s := string(b)
					vals[i] = &s
				}
			}
			page.Rows = append(page.Rows, vals)
		}
		if len(page.Rows) > limit {
			page.Rows, page.HasMore = page.Rows[:limit], true
		}
		return rows.Err()
	})
	return page, err
}
