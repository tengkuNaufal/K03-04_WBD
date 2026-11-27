package web

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"dbmp/internal/authz"
	"dbmp/internal/model"
	"dbmp/internal/obs"
	"dbmp/internal/targetdb"
	"dbmp/internal/workflow"
)

type migrationView struct {
	model.Migration
	Actions  []string `json:"actions"`
	Editable bool     `json:"editable"`
}

func view(st workflow.State, m model.Migration, canEdit bool) migrationView {
	v := migrationView{Migration: m, Actions: []string{}}
	if !canEdit {
		return v
	}
	if acts := workflow.Actions(st, m); acts != nil {
		v.Actions = acts
	}
	v.Editable = m.Status == model.MigrationDraft && !m.Locked
	for _, r := range st.Requests {
		if r.MigrationID == m.ID && model.Open(r.Status) {
			v.Editable = false
		}
	}
	return v
}

func (s *Server) migrationList(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionView)
	if !ok {
		return
	}
	st, err := s.Store.State(r.Context(), h.DB.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	canEdit := authz.Allows(h.Level, authz.ActionEdit)
	out := []migrationView{}
	for _, m := range st.Migrations {
		out = append(out, view(st, m, canEdit))
	}
	writeJSON(w, http.StatusOK, out)
}

type migrationInput struct {
	Name               string `json:"name"`
	UpSQL              string `json:"up_sql"`
	DownSQL            string `json:"down_sql"`
	Irreversible       bool   `json:"irreversible"`
	IrreversibleReason string `json:"irreversible_reason"`
	Mode               string `json:"mode"`
	BatchTable         string `json:"batch_table"`
	BatchColumn        string `json:"batch_column"`
	BatchSize          int    `json:"batch_size"`
}

func (in migrationInput) migration(dbID int64) model.Migration {
	m := model.Migration{DatabaseID: dbID, Name: strings.TrimSpace(in.Name), UpSQL: in.UpSQL, DownSQL: in.DownSQL, Mode: in.Mode,
		BatchTable: strings.TrimSpace(in.BatchTable), BatchColumn: strings.TrimSpace(in.BatchColumn), BatchSize: in.BatchSize}
	if in.Irreversible {
		m.IrreversibleReason = strings.TrimSpace(in.IrreversibleReason)
		if m.IrreversibleReason == "" {
			m.IrreversibleReason = " "
		}
	}
	if m.Mode == "" {
		m.Mode = model.ModeNormal
	}
	if m.Mode != model.ModeBatch {
		m.BatchTable, m.BatchColumn, m.BatchSize = "", "", 0
	}
	return m
}

func readMigration(r *http.Request, dbID int64) (model.Migration, error) {
	var in migrationInput
	if err := decode(r, &in); err != nil {
		return model.Migration{}, err
	}
	m := in.migration(dbID)
	if strings.TrimSpace(m.IrreversibleReason) == "" && in.Irreversible {
		return m, model.Invalid("Alasan irreversible wajib diisi.")
	}
	return m, targetdb.Validate(m)
}

func (s *Server) migrationCreate(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionEdit)
	if !ok {
		return
	}
	m, err := readMigration(r, h.DB.ID)
	if err == nil {
		m.CreatedBy = principal(r).Username
		m.ID, err = s.Store.CreateMigration(r.Context(), m)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	slog.InfoContext(r.Context(), "migration created", "event", "migration.create", "actor", m.CreatedBy,
		"database", h.DB.ID, "migration_id", m.ID, "status", "draft")
	writeJSON(w, http.StatusCreated, map[string]int64{"id": m.ID})
}

func (s *Server) migrationDetail(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionView)
	if !ok {
		return
	}
	st, err := s.Store.State(r.Context(), h.DB.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id := pathID(r, "mID")
	i := slices.IndexFunc(st.Migrations, func(m model.Migration) bool { return m.ID == id })
	if i < 0 {
		s.fail(w, r, model.ErrNotFound)
		return
	}
	reqs := []model.Request{}
	for _, rq := range slices.Backward(st.Requests) {
		if rq.MigrationID == id {
			reqs = append(reqs, rq)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"migration": view(st, st.Migrations[i], authz.Allows(h.Level, authz.ActionEdit)), "requests": reqs,
	})
}

func (s *Server) migrationUpdate(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionEdit)
	if !ok {
		return
	}
	m, err := readMigration(r, h.DB.ID)
	m.ID = pathID(r, "mID")
	if err == nil {
		err = s.Store.UpdateMigration(r.Context(), m)
	}
	if errors.Is(err, model.ErrNotFound) {
		err = model.Invalid("Migration tidak dapat diubah: SQL migration yang pernah dieksekusi atau sedang diajukan bersifat immutable. Buat migration baru untuk perbaikan.")
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	slog.InfoContext(r.Context(), "migration updated", "event", "migration.update", "actor", principal(r).Username,
		"database", h.DB.ID, "migration_id", m.ID, "status", "draft")
	writeOK(w, "Migration tersimpan.")
}

func (s *Server) migrationDelete(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionEdit)
	if !ok {
		return
	}
	if err := s.Store.DeleteMigration(r.Context(), h.DB.ID, pathID(r, "mID")); err != nil {
		s.fail(w, r, model.Invalid("Hanya draft yang belum pernah dieksekusi atau diajukan yang dapat dihapus."))
		return
	}
	slog.InfoContext(r.Context(), "migration deleted", "event", "migration.delete", "actor", principal(r).Username,
		"database", h.DB.ID, "migration_id", pathID(r, "mID"), "status", "deleted")
	writeOK(w, "Migration dihapus.")
}

func (s *Server) migrationValidate(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionEdit)
	if !ok {
		return
	}
	var in struct {
		Analyze bool `json:"analyze"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	m, err := s.Store.GetMigration(r.Context(), h.DB.ID, pathID(r, "mID"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	res := map[string]any{"valid": true, "error": "", "up": []model.ExplainResult{}, "down": []model.ExplainResult{}}
	if err := targetdb.Validate(m); err != nil {
		res["valid"], res["error"] = false, err.Error()
		writeJSON(w, http.StatusOK, res)
		return
	}
	up, down := targetdb.Split(m.UpSQL), targetdb.Split(m.DownSQL)
	out, err := s.Targets.Explain(r.Context(), h.DB.ID, append(up, down...), in.Analyze)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, e := range out {
		if e.Error != "" {
			res["valid"] = false
		}
	}
	res["up"], res["down"] = out[:len(up)], out[len(up):]
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) requestSubmit(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionEdit)
	if !ok {
		return
	}
	var in struct {
		Action          string `json:"action"`
		Note            string `json:"note"`
		TargetRequestID int64  `json:"target_request_id"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	p := principal(r)
	id, err := s.Store.SubmitRequest(r.Context(), h.DB.ID, pathID(r, "mID"), in.Action, in.TargetRequestID,
		strings.TrimSpace(in.Note), *p, workflow.Fingerprint)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	slog.InfoContext(r.Context(), "change request submitted", "event", "request.submit", "actor", p.Username,
		"database", h.DB.ID, "migration_id", pathID(r, "mID"), "request_id", id, "action", in.Action, "status", model.ReqPendingReview)
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) requestList(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionView)
	if !ok {
		return
	}
	mid, _ := strconv.ParseInt(r.URL.Query().Get("migration_id"), 10, 64)
	rs, err := s.Store.ListRequests(r.Context(), h.DB.ID, mid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if rs == nil {
		rs = []model.Request{}
	}
	writeJSON(w, http.StatusOK, rs)
}

func (s *Server) requestDetail(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionView)
	if !ok {
		return
	}
	ctx, id := r.Context(), pathID(r, "rID")
	req, err := s.Store.GetRequest(ctx, h.DB.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	execs, err := s.Store.ListExecutions(ctx, h.DB.ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var job *model.Job
	if req.JobID != nil {
		if j, err := s.Store.GetJob(ctx, h.DB.ID, *req.JobID); err == nil {
			job = &j
		}
	}
	m, err := s.Store.GetMigration(ctx, h.DB.ID, req.MigrationID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var pending []authz.PendingApproval
	for _, a := range req.Approvals {
		if a.Decision == "" {
			pending = append(pending, authz.PendingApproval{GroupID: a.GroupID, LeaderID: a.LeaderID})
		}
	}
	canEdit := authz.Allows(h.Level, authz.ActionEdit)
	if execs == nil {
		execs = []model.Execution{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"request": req, "migration": m, "executions": execs, "job": job,
		"can_approve": req.Status == model.ReqPendingReview && len(authz.ApprovableGroups(principal(r).UserID, pending)) > 0,
		"can_execute": canEdit && req.Status == model.ReqApproved,
		"can_cancel":  canEdit && (req.Status == model.ReqPendingReview || req.Status == model.ReqApproved),
		"can_resume":  canEdit && model.Blocking(req.Status),
	})
}

func (s *Server) decide(w http.ResponseWriter, r *http.Request, approve bool) {
	h, ok := s.loadDB(w, r, authz.ActionView)
	if !ok {
		return
	}
	var in struct {
		Comment string `json:"comment"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	p := principal(r)
	status, err := s.Store.Decide(r.Context(), h.DB.ID, pathID(r, "rID"), *p, approve, strings.TrimSpace(in.Comment))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	event := "request.approve"
	if !approve {
		event = "request.reject"
	}
	slog.InfoContext(r.Context(), "approval decision", "event", event, "actor", p.Username, "database", h.DB.ID,
		"request_id", pathID(r, "rID"), "status", status)
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

func (s *Server) requestApprove(w http.ResponseWriter, r *http.Request) { s.decide(w, r, true) }
func (s *Server) requestReject(w http.ResponseWriter, r *http.Request)  { s.decide(w, r, false) }

func (s *Server) requestCancel(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionEdit)
	if !ok {
		return
	}
	if err := s.Store.CancelRequest(r.Context(), h.DB.ID, pathID(r, "rID")); err != nil {
		s.fail(w, r, err)
		return
	}
	slog.InfoContext(r.Context(), "change request cancelled", "event", "request.cancel", "actor", principal(r).Username,
		"database", h.DB.ID, "request_id", pathID(r, "rID"), "status", model.ReqCancelled)
	writeOK(w, "Pengajuan dibatalkan.")
}

func (s *Server) requestExecute(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionEdit)
	if !ok {
		return
	}
	id := pathID(r, "rID")
	ctx, span := obs.Tracer.Start(r.Context(), "queue.publish", trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(attribute.Int64("request.id", id), attribute.Int64("database.id", h.DB.ID)))
	defer span.End()
	jobID, err := s.Store.Enqueue(ctx, h.DB.ID, id, obs.Inject(ctx), func(st workflow.State, req model.Request) error {
		i := slices.IndexFunc(st.Migrations, func(m model.Migration) bool { return m.ID == req.MigrationID })
		if i < 0 {
			return model.ErrNotFound
		}
		var target int64
		if req.TargetRequestID != nil {
			target = *req.TargetRequestID
		}
		_, err := workflow.Check(st, st.Migrations[i], req.Action, target, req.ID)
		var he *workflow.HeldError
		if errors.As(err, &he) {
			return nil
		}
		return err
	})
	if err != nil {
		obs.End(span, err)
		s.fail(w, r, err)
		return
	}
	span.SetAttributes(attribute.Int64("job.id", jobID))
	slog.InfoContext(ctx, "job published", "event", "job.enqueue", "actor", principal(r).Username, "database", h.DB.ID,
		"request_id", id, "job_id", jobID, "status", model.JobQueued)
	writeJSON(w, http.StatusAccepted, map[string]int64{"job_id": jobID})
}

func (s *Server) jobDetail(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionView)
	if !ok {
		return
	}
	j, err := s.Store.GetJob(r.Context(), h.DB.ID, pathID(r, "jobID"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	req, err := s.Store.GetRequest(r.Context(), h.DB.ID, j.RequestID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": j, "request": req})
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionView)
	if !ok {
		return
	}
	execs, err := s.Store.ListExecutions(r.Context(), h.DB.ID, 0)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if r.URL.Query().Get("hide_rollback") == "1" {
		execs = workflow.HideRollbacks(execs)
	}
	if execs == nil {
		execs = []model.Execution{}
	}
	writeJSON(w, http.StatusOK, execs)
}
