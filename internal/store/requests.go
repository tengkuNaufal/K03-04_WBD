package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"dbmp/internal/authz"
	"dbmp/internal/model"
	"dbmp/internal/workflow"
)

const reqCols = `r.id, r.database_id, r.migration_id, m.name, r.action, r.direction, r.target_request_id, r.series_id,
	r.undo_series_id, r.status, r.note, r.sql_hash, r.scope_max, r.batches_done, r.rows_done, r.error,
	r.requested_by, r.requested_by_id, r.created_at, r.updated_at, (SELECT j.id FROM jobs j WHERE j.request_id = r.id)`

const reqFrom = ` FROM change_requests r JOIN migrations m ON m.id = r.migration_id `

func scanRequest(row pgx.Row) (model.Request, error) {
	var r model.Request
	err := row.Scan(&r.ID, &r.DatabaseID, &r.MigrationID, &r.MigrationName, &r.Action, &r.Direction, &r.TargetRequestID,
		&r.SeriesID, &r.UndoSeriesID, &r.Status, &r.Note, &r.SQLHash, &r.ScopeMax, &r.BatchesDone, &r.RowsDone, &r.Error,
		&r.RequestedBy, &r.RequestedByID, &r.CreatedAt, &r.UpdatedAt, &r.JobID)
	return r, mapErr(err)
}

func collectRequests(rows pgx.Rows) ([]model.Request, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Request, error) { return scanRequest(r) })
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadState(ctx context.Context, q querier, databaseID int64) (workflow.State, error) {
	rows, _ := q.Query(ctx, `SELECT `+migCols+` FROM migrations m WHERE m.database_id = $1 ORDER BY m.version NULLS LAST, m.id`, databaseID)
	ms, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Migration, error) { return scanMigration(r) })
	if err != nil {
		return workflow.State{}, err
	}
	rows, _ = q.Query(ctx, `SELECT `+reqCols+reqFrom+`WHERE r.database_id = $1 ORDER BY r.id`, databaseID)
	rs, err := collectRequests(rows)
	return workflow.State{Migrations: ms, Requests: rs}, err
}

func (s *Store) State(ctx context.Context, databaseID int64) (workflow.State, error) {
	return loadState(ctx, s.pool, databaseID)
}

func (s *Store) SubmitRequest(ctx context.Context, databaseID, migrationID int64, action string, targetID int64,
	note string, by authz.Principal, sqlHash func(model.Migration) string) (int64, error) {
	var id int64
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM databases WHERE id = $1 FOR UPDATE`, databaseID); err != nil {
			return err
		}
		st, err := loadState(ctx, tx, databaseID)
		if err != nil {
			return err
		}
		var m *model.Migration
		for i := range st.Migrations {
			if st.Migrations[i].ID == migrationID {
				m = &st.Migrations[i]
			}
		}
		if m == nil {
			return model.ErrNotFound
		}
		plan, err := workflow.Check(st, *m, action, targetID, 0)
		if err != nil {
			return err
		}
		var batches int
		var rows int64
		if action == model.ActionResume { // counters continue the series
			for _, r := range st.Requests {
				if r.ID == targetID {
					batches, rows = r.BatchesDone, r.RowsDone
				}
			}
		}
		err = tx.QueryRow(ctx, `INSERT INTO change_requests (database_id, migration_id, action, direction, target_request_id,
			series_id, undo_series_id, status, note, sql_hash, batches_done, rows_done, requested_by, requested_by_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending_review', $8, $9, $10, $11, $12, $13) RETURNING id`,
			databaseID, migrationID, action, plan.Direction, plan.TargetRequestID, plan.SeriesID, plan.UndoSeriesID,
			note, sqlHash(*m), batches, rows, by.Username, by.UserID).Scan(&id)
		if err != nil {
			return err
		}
		if plan.SeriesID == 0 {
			if _, err := tx.Exec(ctx, `UPDATE change_requests SET series_id = id WHERE id = $1`, id); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO request_approvals (request_id, group_id, group_name)
			SELECT $1, g.id, g.name FROM groups g JOIN group_database_access a ON a.group_id = g.id
			WHERE a.database_id = $2`, id, databaseID)
		return err
	})
	return id, err
}

func (s *Store) approvals(ctx context.Context, requestID int64) ([]model.Approval, error) {
	rows, _ := s.pool.Query(ctx, `
		SELECT a.group_id, a.group_name, g.leader_id, COALESCE(u.username, ''), COALESCE(a.decision, ''),
		       COALESCE(a.decided_by, ''), a.decided_at, a.comment
		FROM request_approvals a JOIN groups g ON g.id = a.group_id LEFT JOIN users u ON u.id = g.leader_id
		WHERE a.request_id = $1 ORDER BY a.group_name`, requestID)
	return pgx.CollectRows(rows, pgx.RowToStructByPos[model.Approval])
}

func (s *Store) GetRequest(ctx context.Context, databaseID, id int64) (model.Request, error) {
	r, err := scanRequest(s.pool.QueryRow(ctx, `SELECT `+reqCols+reqFrom+`WHERE r.id = $1 AND r.database_id = $2`, id, databaseID))
	if err == nil {
		r.Approvals, err = s.approvals(ctx, id)
	}
	return r, err
}

func (s *Store) ListRequests(ctx context.Context, databaseID, migrationID int64) ([]model.Request, error) {
	rows, _ := s.pool.Query(ctx, `SELECT `+reqCols+reqFrom+`WHERE r.database_id = $1 AND ($2 = 0 OR r.migration_id = $2)
		ORDER BY r.id DESC LIMIT 200`, databaseID, migrationID)
	return collectRequests(rows)
}

func (s *Store) AwaitingApproval(ctx context.Context, userID int64) ([]model.Request, error) {
	rows, _ := s.pool.Query(ctx, `SELECT `+reqCols+reqFrom+`WHERE r.status = 'pending_review' AND EXISTS (
		SELECT 1 FROM request_approvals a JOIN groups g ON g.id = a.group_id
		WHERE a.request_id = r.id AND a.decision IS NULL AND g.leader_id = $1) ORDER BY r.id`, userID)
	return collectRequests(rows)
}

func (s *Store) Decide(ctx context.Context, databaseID, id int64, by authz.Principal, approve bool, comment string) (string, error) {
	var status string
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT status FROM change_requests WHERE id = $1 AND database_id = $2 FOR UPDATE`,
			id, databaseID).Scan(&status); err != nil {
			return mapErr(err)
		}
		if status != model.ReqPendingReview {
			return model.Invalid("Pengajuan ini tidak sedang menunggu review.")
		}
		rows, _ := tx.Query(ctx, `SELECT a.group_id, g.leader_id FROM request_approvals a JOIN groups g ON g.id = a.group_id
			WHERE a.request_id = $1 AND a.decision IS NULL`, id)
		pending, err := pgx.CollectRows(rows, pgx.RowToStructByPos[authz.PendingApproval])
		if err != nil {
			return err
		}
		groups := authz.ApprovableGroups(by.UserID, pending)
		if len(groups) == 0 {
			return authz.ErrForbidden
		}
		decision := "approved"
		if !approve {
			decision = "rejected"
		}
		if _, err := tx.Exec(ctx, `UPDATE request_approvals SET decision = $3, decided_by = $4, decided_at = now(), comment = $5
			WHERE request_id = $1 AND group_id = ANY($2)`, id, groups, decision, by.Username, comment); err != nil {
			return err
		}
		switch {
		case !approve:
			status = model.ReqRejected
		case len(groups) == len(pending):
			status = model.ReqApproved
		}
		_, err = tx.Exec(ctx, `UPDATE change_requests SET status = $2, updated_at = now() WHERE id = $1`, id, status)
		return err
	})
	return status, err
}

func (s *Store) CancelRequest(ctx context.Context, databaseID, id int64) error {
	err := s.exec(ctx, `UPDATE change_requests SET status = 'cancelled', updated_at = now()
		WHERE id = $1 AND database_id = $2 AND status IN ('pending_review', 'approved')`, id, databaseID)
	if err == model.ErrNotFound {
		return model.Invalid("Hanya pengajuan yang belum masuk antrean yang dapat dibatalkan.")
	}
	return err
}

func (s *Store) Enqueue(ctx context.Context, databaseID, id int64, traceContext map[string]string,
	check func(workflow.State, model.Request) error) (int64, error) {
	var jobID int64
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM databases WHERE id = $1 FOR UPDATE`, databaseID); err != nil {
			return err
		}
		r, err := scanRequest(tx.QueryRow(ctx, `SELECT `+reqCols+reqFrom+`WHERE r.id = $1 AND r.database_id = $2`, id, databaseID))
		if err != nil {
			return err
		}
		if r.Status != model.ReqApproved {
			return model.Invalid("Hanya pengajuan yang sudah disetujui semua leader yang dapat dijalankan.")
		}
		st, err := loadState(ctx, tx, databaseID)
		if err == nil {
			err = check(st, r)
		}
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `INSERT INTO jobs (request_id, database_id, trace_context) VALUES ($1, $2, $3) RETURNING id`,
			id, databaseID, traceContext).Scan(&jobID)
		if err != nil {
			return mapErr(err)
		}
		_, err = tx.Exec(ctx, `UPDATE change_requests SET status = 'queued', updated_at = now() WHERE id = $1`, id)
		return err
	})
	return jobID, err
}

const execCols = `id, request_id, job_id, database_id, migration_id, migration_name, migration_version, action, direction,
	attempt, reverts_execution_id, sql_text, status, batches, rows_affected, error, executed_by, trace_id, started_at, finished_at`

func (s *Store) StartExecution(ctx context.Context, e model.Execution) (int64, error) {
	return s.insert(ctx, `INSERT INTO executions (request_id, job_id, database_id, migration_id, migration_name,
		migration_version, action, direction, attempt, sql_text, status, executed_by, trace_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'running', $11, $12) RETURNING id`,
		e.RequestID, e.JobID, e.DatabaseID, e.MigrationID, e.MigrationName, e.MigrationVersion, e.Action, e.Direction,
		e.Attempt, e.SQL, e.ExecutedBy, e.TraceID)
}

func (s *Store) ListExecutions(ctx context.Context, databaseID, requestID int64) ([]model.Execution, error) {
	rows, _ := s.pool.Query(ctx, `SELECT `+execCols+` FROM executions WHERE database_id = $1 AND ($2 = 0 OR request_id = $2)
		ORDER BY id`, databaseID, requestID)
	return pgx.CollectRows(rows, pgx.RowToStructByPos[model.Execution])
}

func (s *Store) GetRun(ctx context.Context, requestID int64) (model.Request, model.Migration, error) {
	r, err := scanRequest(s.pool.QueryRow(ctx, `SELECT `+reqCols+reqFrom+`WHERE r.id = $1`, requestID))
	if err != nil {
		return r, model.Migration{}, err
	}
	r.Approvals, err = s.approvals(ctx, requestID)
	if err != nil {
		return r, model.Migration{}, err
	}
	m, err := s.GetMigration(ctx, r.DatabaseID, r.MigrationID)
	return r, m, err
}

func (s *Store) SetRequestRunning(ctx context.Context, id int64) error {
	return s.exec(ctx, `UPDATE change_requests SET status = 'running', error = '', updated_at = now() WHERE id = $1`, id)
}

func (s *Store) SetScope(ctx context.Context, id int64, scopeMax int64) error {
	return s.exec(ctx, `UPDATE change_requests SET scope_max = $2 WHERE id = $1`, id, scopeMax)
}

func (s *Store) SetProgress(ctx context.Context, id int64, batches int, rows int64) error {
	return s.exec(ctx, `UPDATE change_requests SET batches_done = $2, rows_done = $3, updated_at = now() WHERE id = $1`, id, batches, rows)
}

type Outcome struct {
	Job             model.Job
	Request         model.Request
	ExecutionID     int64
	ReqStatus       string
	ExecStatus      string
	MigrationStatus string
	Progressed      bool
	Batches         int
	Rows            int64
	AttemptBatches  int
	AttemptRows     int64
	Error           string
	RetryAfter      time.Duration
}

func (s *Store) Finish(ctx context.Context, o Outcome) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		r := o.Request
		if o.MigrationStatus != "" {
			if _, err := tx.Exec(ctx, `UPDATE migrations SET status = $2, version = COALESCE(version,
				(SELECT COALESCE(MAX(version), 0) + 1 FROM migrations WHERE database_id = $3)) WHERE id = $1`,
				r.MigrationID, o.MigrationStatus, r.DatabaseID); err != nil {
				return err
			}
		}
		if o.ExecutionID != 0 {
			var reverts *int64
			if r.Direction == model.DirDown && o.ExecStatus == model.ExecSuccess {
				_ = tx.QueryRow(ctx, `SELECT id FROM executions WHERE migration_id = $1 AND direction = 'up'
					AND status IN ('success', 'partial') ORDER BY id DESC LIMIT 1`, r.MigrationID).Scan(&reverts)
			}
			if _, err := tx.Exec(ctx, `UPDATE executions SET status = $2, batches = $3, rows_affected = $4, error = $5,
				reverts_execution_id = $6, finished_at = now(),
				migration_version = (SELECT version FROM migrations WHERE id = migration_id) WHERE id = $1`,
				o.ExecutionID, o.ExecStatus, o.AttemptBatches, o.AttemptRows, o.Error, reverts); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE change_requests SET status = $2, error = $3, batches_done = $4, rows_done = $5,
			updated_at = now() WHERE id = $1`, r.ID, o.ReqStatus, o.Error, o.Batches, o.Rows); err != nil {
			return err
		}
		if r.TargetRequestID != nil && (o.Progressed || o.ReqStatus == model.ReqSuccess) {
			if _, err := tx.Exec(ctx, `UPDATE change_requests SET status = 'superseded', updated_at = now()
				WHERE id = $1 AND status IN ('partial', 'partially_reverted', 'uncertain')`, *r.TargetRequestID); err != nil {
				return err
			}
		}
		if o.RetryAfter > 0 {
			_, err := tx.Exec(ctx, `UPDATE jobs SET status = 'queued', locked_by = NULL, run_after = now() + make_interval(secs => $2)
				WHERE id = $1`, o.Job.ID, o.RetryAfter.Seconds())
			return err
		}
		jobStatus := model.JobDone
		if o.ReqStatus != model.ReqSuccess {
			jobStatus = model.JobFailed
		}
		_, err := tx.Exec(ctx, `UPDATE jobs SET status = $2, locked_by = NULL, finished_at = now() WHERE id = $1`, o.Job.ID, jobStatus)
		return err
	})
}
