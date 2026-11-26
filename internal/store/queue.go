package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"dbmp/internal/model"
)

const jobCols = `id, request_id, database_id, status, attempts, max_attempts, run_after, COALESCE(locked_by, ''),
	trace_context, enqueued_at, started_at, finished_at`

func scanJob(row pgx.Row) (model.Job, error) {
	var j model.Job
	err := row.Scan(&j.ID, &j.RequestID, &j.DatabaseID, &j.Status, &j.Attempts, &j.MaxAttempts, &j.RunAfter, &j.LockedBy,
		&j.TraceContext, &j.EnqueuedAt, &j.StartedAt, &j.FinishedAt)
	return j, mapErr(err)
}

func (s *Store) ClaimJob(ctx context.Context, workerID string) (model.Job, error) {
	return scanJob(s.pool.QueryRow(ctx, `
		UPDATE jobs SET status = 'running', attempts = attempts + 1, locked_by = $1, heartbeat_at = now(),
		       started_at = COALESCE(started_at, now())
		WHERE id = (
			SELECT j.id FROM jobs j JOIN change_requests cr ON cr.id = j.request_id
			WHERE j.status = 'queued' AND j.run_after <= now()
			  AND NOT EXISTS (SELECT 1 FROM jobs o WHERE o.database_id = j.database_id AND o.status = 'running')
			  AND NOT EXISTS (SELECT 1 FROM change_requests b WHERE b.database_id = j.database_id
			        AND b.status IN ('partial', 'partially_reverted', 'uncertain')
			        AND b.id <> cr.id AND b.id IS DISTINCT FROM cr.target_request_id)
			ORDER BY j.id FOR UPDATE OF j SKIP LOCKED LIMIT 1)
		RETURNING `+jobCols, workerID))
}

func (s *Store) ReleaseJob(ctx context.Context, jobID int64, delay time.Duration) error {
	return s.exec(ctx, `UPDATE jobs SET status = 'queued', attempts = attempts - 1, locked_by = NULL,
		run_after = now() + make_interval(secs => $2) WHERE id = $1 AND status = 'running'`, jobID, delay.Seconds())
}

func (s *Store) Heartbeat(ctx context.Context, jobID int64, workerID string) error {
	return s.exec(ctx, `UPDATE jobs SET heartbeat_at = now() WHERE id = $1 AND locked_by = $2`, jobID, workerID)
}

func (s *Store) RecoverStale(ctx context.Context, after time.Duration) ([]int64, error) {
	var ids []int64
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, _ := tx.Query(ctx, `UPDATE jobs SET status = 'queued', locked_by = NULL, run_after = now()
			WHERE status = 'running' AND heartbeat_at < now() - make_interval(secs => $1) RETURNING request_id`, after.Seconds())
		var err error
		if ids, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil || len(ids) == 0 {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE change_requests SET status = 'uncertain', updated_at = now(),
			error = 'Worker berhenti sebelum hasil dicatat; bukti eksekusi diperiksa sebelum retry.' WHERE id = ANY($1)`, ids); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE executions SET status = 'uncertain', finished_at = now(),
			error = 'Worker berhenti sebelum hasil dicatat.' WHERE request_id = ANY($1) AND status = 'running'`, ids)
		return err
	})
	return ids, err
}

func (s *Store) GetJob(ctx context.Context, databaseID, id int64) (model.Job, error) {
	return scanJob(s.pool.QueryRow(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = $1 AND database_id = $2`, id, databaseID))
}

// JobCounts returns the number of jobs per status (for metrics).
func (s *Store) JobCounts(ctx context.Context) (map[string]int, error) {
	rows, _ := s.pool.Query(ctx, `SELECT status, count(*) FROM jobs GROUP BY status`)
	out := map[string]int{model.JobQueued: 0, model.JobRunning: 0, model.JobDone: 0, model.JobFailed: 0}
	var status string
	var n int
	_, err := pgx.ForEachRow(rows, []any{&status, &n}, func() error { out[status] = n; return nil })
	return out, err
}
