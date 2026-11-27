package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"dbmp/internal/model"
	"dbmp/internal/obs"
	"dbmp/internal/store"
	"dbmp/internal/targetdb"
	"dbmp/internal/workflow"
)

type Worker struct {
	Store      *store.Store
	Targets    *targetdb.Manager
	ID         string
	Poll       time.Duration 
	Heartbeat  time.Duration 
	StaleAfter time.Duration 
}

func (w *Worker) defaults() {
	if w.Poll == 0 {
		w.Poll = time.Second
	}
	if w.Heartbeat == 0 {
		w.Heartbeat = 5 * time.Second
	}
	if w.StaleAfter == 0 {
		w.StaleAfter = 30 * time.Second
	}
}

func (w *Worker) Run(ctx context.Context) {
	w.defaults()
	slog.Info("worker started", "event", "worker.start", "worker", w.ID)
	for ctx.Err() == nil {
		if ids, err := w.Store.RecoverStale(ctx, w.StaleAfter); err != nil {
			slog.Error("recover stale jobs failed", "event", "queue.recover", "status", "error", "error", err)
		} else if len(ids) > 0 {
			slog.Warn("recovered jobs of a stopped worker", "event", "queue.recover", "status", "uncertain", "request_ids", ids)
		}
		found, err := w.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("claim job failed", "event", "queue.claim", "status", "error", "error", err)
		}
		if !found {
			select {
			case <-ctx.Done():
			case <-time.After(w.Poll):
			}
		}
	}
	slog.Info("worker stopped", "event", "worker.stop", "worker", w.ID)
}

func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	w.defaults()
	job, err := w.Store.ClaimJob(ctx, w.ID)
	if errors.Is(err, model.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	w.process(ctx, job)
	return true, nil
}

func (w *Worker) process(ctx context.Context, job model.Job) {
	ctx = obs.Extract(ctx, job.TraceContext)
	ctx, span := obs.Tracer.Start(ctx, "queue.process", trace.WithSpanKind(trace.SpanKindConsumer), trace.WithAttributes(
		attribute.Int64("job.id", job.ID), attribute.Int64("request.id", job.RequestID),
		attribute.Int64("database.id", job.DatabaseID), attribute.Int("job.attempt", job.Attempts)))
	defer span.End()
	log := slog.With("job_id", job.ID, "request_id", job.RequestID, "database", job.DatabaseID, "attempt", job.Attempts, "worker", w.ID)
	saveCtx := context.WithoutCancel(ctx)

	queued := job.EnqueuedAt
	if job.RunAfter.After(queued) {
		queued = job.RunAfter
	}
	obs.QueueWait.Observe(time.Since(queued).Seconds())
	log.InfoContext(ctx, "job claimed", "event", "job.claimed", "status", "running", "queue_wait_ms", time.Since(queued).Milliseconds())

	unlock, ok, err := w.lock(ctx, job.DatabaseID)
	if err != nil || !ok {
		log.InfoContext(ctx, "database lock busy, job back in queue", "event", "lock.busy", "status", "waiting", "error", err)
		_ = w.Store.ReleaseJob(saveCtx, job.ID, 2*time.Second)
		return
	}
	defer unlock()

	hbCtx, stopHB := context.WithCancel(ctx)
	defer stopHB()
	go w.heartbeat(hbCtx, job.ID)

	req, mig, err := w.Store.GetRun(ctx, job.RequestID)
	if err != nil {
		log.ErrorContext(ctx, "load request failed", "event", "job.error", "status", "error", "error", err)
		_ = w.Store.ReleaseJob(saveCtx, job.ID, 5*time.Second)
		return
	}
	log = log.With("actor", req.RequestedBy, "migration_id", mig.ID, "action", req.Action, "direction", req.Direction, "mode", mig.Mode)

	if err := w.revalidate(ctx, req, mig); err != nil {
		var he *workflow.HeldError
		if errors.As(err, &he) {
			log.InfoContext(ctx, "database held, job waits", "event", "migration.held", "status", "waiting", "reason", err.Error())
			_ = w.Store.ReleaseJob(saveCtx, job.ID, 5*time.Second)
			return
		}
		log.WarnContext(ctx, "revalidation failed", "event", "migration.revalidate", "status", "failed", "error", err)
		obs.End(span, err)
		w.finish(saveCtx, log, store.Outcome{Job: job, Request: req, ReqStatus: model.ReqFailed,
			Error: "Revalidasi gagal: " + err.Error(), Batches: req.BatchesDone, Rows: req.RowsDone}, mig, 0)
		return
	}
	if err := w.Store.SetRequestRunning(ctx, req.ID); err != nil {
		log.ErrorContext(ctx, "mark running failed", "event", "job.error", "status", "error", "error", err)
	}
	start := time.Now()
	o := w.execute(ctx, log, job, req, mig)
	if o.Error != "" {
		span.SetAttributes(attribute.String("migration.error", o.Error))
	}
	w.finish(saveCtx, log, o, mig, time.Since(start))
}

func (w *Worker) finish(ctx context.Context, log *slog.Logger, o store.Outcome, mig model.Migration, took time.Duration) {
	if err := w.Store.Finish(ctx, o); err != nil {
		log.ErrorContext(ctx, "record outcome failed", "event", "job.record", "status", "error", "error", err)
		return
	}
	result := o.ReqStatus
	if o.RetryAfter > 0 {
		result = "retry"
		obs.JobRetries.Inc()
	} else {
		obs.JobsCompleted.WithLabelValues(o.ReqStatus).Inc()
	}
	if took > 0 {
		obs.MigrationDuration.WithLabelValues(o.Request.Action, mig.Mode, result).Observe(took.Seconds())
	}
	log.InfoContext(ctx, "job finished", "event", "job.finished", "status", result, "execution_id", o.ExecutionID,
		"batches", o.Batches, "rows", o.Rows, "duration_ms", took.Milliseconds(), "retry_in_s", o.RetryAfter.Seconds(), "error", o.Error)
}

const lockSpace = int64(0x64626d70) << 32 

func (w *Worker) lock(ctx context.Context, databaseID int64) (unlock func(), ok bool, err error) {
	ctx, span := obs.Tracer.Start(ctx, "lock.acquire", trace.WithAttributes(attribute.Int64("database.id", databaseID)))
	defer func() { span.SetAttributes(attribute.Bool("lock.acquired", ok)); obs.End(span, err) }()
	start := time.Now()
	conn, err := w.Store.Pool().Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockSpace|databaseID).Scan(&ok); err != nil || !ok {
		conn.Release()
		return nil, false, err
	}
	obs.LockWait.Observe(time.Since(start).Seconds())
	return func() { release(conn, databaseID) }, true, nil
}

func release(conn *pgxpool.Conn, databaseID int64) {
	ctx := context.Background()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, lockSpace|databaseID); err != nil {
		_ = conn.Conn().Close(ctx)
	}
	conn.Release()
}

func (w *Worker) heartbeat(ctx context.Context, jobID int64) {
	t := time.NewTicker(w.Heartbeat)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.Store.Heartbeat(ctx, jobID, w.ID); err != nil && ctx.Err() == nil {
				slog.Warn("heartbeat failed", "event", "job.heartbeat", "job_id", jobID, "status", "error", "error", err)
			}
		}
	}
}

func (w *Worker) revalidate(ctx context.Context, req model.Request, mig model.Migration) (err error) {
	ctx, span := obs.Tracer.Start(ctx, "migration.revalidate")
	defer func() { obs.End(span, err) }()
	if !model.Approved(req.Approvals) {
		return errors.New("approval belum lengkap")
	}
	groups, err := w.Store.AccessGroups(ctx, req.DatabaseID)
	if err != nil {
		return err
	}
	approved := map[int64]bool{}
	for _, a := range req.Approvals {
		approved[a.GroupID] = true
	}
	for _, g := range groups {
		if !approved[g.ID] {
			return fmt.Errorf("group %s mendapat akses setelah pengajuan dan belum memberi approval; ajukan ulang", g.Name)
		}
	}
	if workflow.Fingerprint(mig) != req.SQLHash {
		return errors.New("definisi migration berubah setelah disetujui")
	}
	if err := targetdb.Validate(mig); err != nil {
		return err
	}
	st, err := w.Store.State(ctx, req.DatabaseID)
	if err != nil {
		return err
	}
	var target int64
	if req.TargetRequestID != nil {
		target = *req.TargetRequestID
	}
	plan, err := workflow.Check(st, mig, req.Action, target, req.ID)
	if err == nil && plan.Direction != req.Direction {
		err = errors.New("arah eksekusi tidak lagi sesuai")
	}
	return err
}

type runResult struct {
	batches, attemptBatches int   
	rows, attemptRows       int64 
}

func (w *Worker) execute(ctx context.Context, log *slog.Logger, job model.Job, req model.Request, mig model.Migration) store.Outcome {
	sql := mig.UpSQL
	if req.Direction == model.DirDown {
		sql = mig.DownSQL
	}
	o := store.Outcome{Job: job, Request: req, Batches: req.BatchesDone, Rows: req.RowsDone}
	execID, err := w.Store.StartExecution(ctx, model.Execution{RequestID: req.ID, JobID: job.ID, DatabaseID: req.DatabaseID,
		MigrationID: mig.ID, MigrationName: mig.Name, MigrationVersion: mig.Version, Action: req.Action, Direction: req.Direction,
		Attempt: job.Attempts, SQL: sql, ExecutedBy: req.RequestedBy, TraceID: obs.TraceID(ctx)})
	if err != nil {
		o.ReqStatus, o.Error, o.RetryAfter = model.ReqQueued, "gagal mencatat eksekusi: "+err.Error(), backoff(job.Attempts)
		return o
	}
	o.ExecutionID = execID
	log = log.With("execution_id", execID)
	log.InfoContext(ctx, "migration execution started", "event", "migration.execute.start", "status", "running", "query", sql)

	var res runResult
	if mig.Mode == model.ModeBatch {
		res, err = w.runBatches(ctx, log, req, mig, sql, execID)
	} else {
		res, err = w.runOnce(ctx, log, req, sql, execID)
	}
	o.Batches, o.Rows, o.AttemptBatches, o.AttemptRows = res.batches, res.rows, res.attemptBatches, res.attemptRows
	o.Progressed = res.attemptBatches > 0

	stopped := model.ReqPartial 
	stoppedExec := model.ExecPartial
	done := model.MigrationApplied
	if req.Direction == model.DirDown {
		stopped, stoppedExec, done = model.ReqPartiallyReverted, model.ExecPartiallyReverted, model.MigrationRolledBack
	}
	var unc *targetdb.UncertainError
	switch {
	case err == nil:
		o.ReqStatus, o.ExecStatus, o.MigrationStatus = model.ReqSuccess, model.ExecSuccess, done
	case errors.As(err, &unc):
		o.ReqStatus, o.ExecStatus, o.Error = model.ReqUncertain, model.ExecUncertain, err.Error()
		if job.Attempts < job.MaxAttempts {
			o.RetryAfter = backoff(job.Attempts)
		}
	case Transient(err) && job.Attempts < job.MaxAttempts:
		o.ReqStatus, o.ExecStatus, o.RetryAfter = model.ReqQueued, model.ExecFailed, backoff(job.Attempts)
		o.Error = "gangguan sementara, dicoba lagi: " + targetdb.PgErrorMessage(err)
		if res.batches > 0 { 
			o.ReqStatus, o.MigrationStatus = stopped, model.MigrationPartial
		}
		if o.Progressed {
			o.ExecStatus = stoppedExec
		}
	default:
		o.Error = targetdb.PgErrorMessage(err)
		o.ReqStatus, o.ExecStatus = model.ReqFailed, model.ExecFailed
		if res.batches > 0 && mig.Mode == model.ModeBatch {
			o.ReqStatus, o.MigrationStatus = stopped, model.MigrationPartial
			if o.Progressed {
				o.ExecStatus = stoppedExec
			}
		}
	}
	return o
}

func (w *Worker) runOnce(ctx context.Context, log *slog.Logger, req model.Request, sql string, execID int64) (runResult, error) {
	if err := w.Targets.EnsureCheckpoints(ctx, req.DatabaseID); err != nil {
		return runResult{}, err
	}
	cps, err := w.Targets.Checkpoints(ctx, req.DatabaseID, req.SeriesID)
	if err != nil {
		return runResult{}, err
	}
	if len(cps) > 0 {
		log.InfoContext(ctx, "already committed, not run again", "event", "migration.evidence", "status", "committed")
		return runResult{rows: cps[0].Rows}, nil
	}
	ctx, span := obs.Tracer.Start(ctx, "migration.transaction")
	n, err := w.Targets.RunOnce(ctx, req.DatabaseID, req.SeriesID, execID, req.Direction, sql)
	obs.End(span, err)
	switch {
	case errors.Is(err, targetdb.ErrAlreadyCommitted):
		log.InfoContext(ctx, "duplicate execution ignored", "event", "migration.evidence", "status", "committed")
		return runResult{}, nil
	case err != nil:
		w.logStop(ctx, log, err)
		return runResult{}, err
	}
	log.InfoContext(ctx, "migration committed", "event", "migration.commit", "status", "committed", "rows", n)
	obs.Rows.WithLabelValues(req.Direction).Add(float64(n))
	return runResult{rows: n, attemptRows: n, attemptBatches: 1}, nil
}

func (w *Worker) logStop(ctx context.Context, log *slog.Logger, err error) {
	var unc *targetdb.UncertainError
	if errors.As(err, &unc) {
		log.WarnContext(ctx, "commit outcome unknown", "event", "migration.commit", "status", "uncertain", "error", err)
		return
	}
	attrs := []any{"event", "migration.rollback", "status", "rolled_back", "error", targetdb.PgErrorMessage(err)}
	if code := targetdb.PgCode(err); code != "" {
		attrs = append(attrs, "sqlstate", code)
	}
	log.WarnContext(ctx, "transaction rolled back", attrs...)
}

func (w *Worker) runBatches(ctx context.Context, log *slog.Logger, req model.Request, mig model.Migration, sql string, execID int64) (runResult, error) {
	var res runResult
	db := req.DatabaseID
	if err := w.Targets.EnsureCheckpoints(ctx, db); err != nil {
		return res, err
	}
	done, err := w.Targets.Checkpoints(ctx, db, req.SeriesID)
	if err != nil {
		return res, err
	}
	for _, c := range done {
		res.batches++
		res.rows += c.Rows
	}

	run := func(batch int, lo, hi int64) error {
		bctx, span := obs.Tracer.Start(ctx, "migration.batch", trace.WithAttributes(
			attribute.Int("batch", batch), attribute.Int64("cursor.from", lo), attribute.Int64("cursor.to", hi)))
		blog := log.With("batch", batch)
		blog.InfoContext(bctx, "batch started", "event", "migration.batch.start", "status", "running", "params", []int64{lo, hi})
		n, err := w.Targets.RunBatch(bctx, db, req.SeriesID, execID, req.Direction, batch, sql, lo, hi)
		obs.End(span, err)
		if errors.Is(err, targetdb.ErrAlreadyCommitted) {
			return fmt.Errorf("batch %d: %w", batch, &targetdb.ConnError{Err: err}) // retry re-reads the checkpoints
		}
		if err != nil {
			w.logStop(bctx, blog, err)
			return err
		}
		res.batches, res.attemptBatches = res.batches+1, res.attemptBatches+1
		res.rows, res.attemptRows = res.rows+n, res.attemptRows+n
		blog.InfoContext(bctx, "batch committed", "event", "migration.batch.commit", "status", "committed", "rows", n,
			"committed_batches", res.batches, "committed_rows", res.rows)
		obs.Batches.WithLabelValues(req.Direction).Inc()
		obs.Rows.WithLabelValues(req.Direction).Add(float64(n))
		if err := w.Store.SetProgress(context.WithoutCancel(ctx), req.ID, res.batches, res.rows); err != nil {
			blog.WarnContext(bctx, "progress not saved", "event", "migration.progress", "status", "error", "error", err)
		}
		return nil
	}

	if req.Direction == model.DirDown {
		if req.UndoSeriesID == nil {
			return res, errors.New("seri batch yang akan dibatalkan tidak diketahui")
		}
		undo, err := w.Targets.Checkpoints(ctx, db, *req.UndoSeriesID)
		if err != nil {
			return res, err
		}
		reverted := map[int]bool{}
		for _, c := range done {
			reverted[c.Batch] = true
		}
		for i := len(undo) - 1; i >= 0; i-- {
			c := undo[i]
			if reverted[c.Batch] || c.Lo == nil || c.Hi == nil {
				continue
			}
			if err := ctx.Err(); err != nil {
				return res, err
			}
			if err := run(c.Batch, *c.Lo, *c.Hi); err != nil {
				return res, err
			}
		}
		return res, nil
	}

	scope := req.ScopeMax
	if scope == nil {
		if scope, err = w.Targets.ScopeMax(ctx, db, mig.BatchTable, mig.BatchColumn); err != nil || scope == nil {
			return res, err 
		}
		if err := w.Store.SetScope(ctx, req.ID, *scope); err != nil {
			return res, err
		}
	}
	lo, next := int64(math.MinInt64), 1
	if n := len(done); n > 0 && done[n-1].Hi != nil {
		lo, next = *done[n-1].Hi, done[n-1].Batch+1
	}
	for {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		hi, ok, err := w.Targets.NextHi(ctx, db, mig.BatchTable, mig.BatchColumn, lo, *scope, mig.BatchSize)
		if err != nil || !ok {
			return res, err
		}
		if err := run(next, lo, hi); err != nil {
			return res, err
		}
		lo, next = hi, next+1
	}
}

func Transient(err error) bool {
	if targetdb.IsConnError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	code := targetdb.PgCode(err)
	switch code {
	case "40001", "40P01", "55P03", "53300", "57P01", "57P02", "57P03":
		return true
	}
	return strings.HasPrefix(code, "08")
}

func backoff(attempt int) time.Duration {
	return min(time.Duration(1<<min(attempt, 6))*time.Second, time.Minute)
}
