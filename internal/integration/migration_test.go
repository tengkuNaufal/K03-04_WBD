package integration

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"dbmp/internal/model"
)

func (w *world) createMigration(c *client, body map[string]any) int64 {
	w.t.Helper()
	if body["mode"] == nil {
		body["mode"] = "normal"
	}
	r := c.post(w.api("/migrations"), body)
	expectStatus(w.t, "create migration", r, http.StatusCreated)
	return r.id(w.t)
}

func (w *world) submit(mID int64, action string, target int64) int64 {
	w.t.Helper()
	r := w.editor.post(w.api("/migrations/%d/requests", mID), map[string]any{"action": action, "target_request_id": target})
	expectStatus(w.t, "submit "+action, r, http.StatusCreated)
	return r.id(w.t)
}

func (w *world) approveAll(rID int64) {
	w.t.Helper()
	for _, c := range []*client{w.viewer, w.editor} {
		expectStatus(w.t, "approve", c.post(w.api("/requests/%d/approve", rID), map[string]string{"comment": "ok"}), http.StatusOK)
	}
}

func (w *world) execute(rID int64) int64 {
	w.t.Helper()
	r := w.editor.post(w.api("/requests/%d/execute", rID), nil)
	expectStatus(w.t, "execute", r, http.StatusAccepted)
	return r.id(w.t)
}

// run submits, approves, executes and drains; it returns the request.
func (w *world) run(mID int64, action string, target int64) model.Request {
	w.t.Helper()
	id := w.submit(mID, action, target)
	w.approveAll(id)
	w.execute(id)
	w.drain()
	return w.request(id)
}

func (w *world) request(id int64) model.Request {
	w.t.Helper()
	r, err := w.store.GetRequest(context.Background(), w.dbID, id)
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

func (w *world) migration(id int64) model.Migration {
	w.t.Helper()
	m, err := w.store.GetMigration(context.Background(), w.dbID, id)
	if err != nil {
		w.t.Fatal(err)
	}
	return m
}

func (w *world) history(hide bool) []model.Execution {
	w.t.Helper()
	path := w.api("/history")
	if hide {
		path += "?hide_rollback=1"
	}
	var out []model.Execution
	r := w.viewer.get(path)
	expectStatus(w.t, "history", r, http.StatusOK)
	r.json(w.t, &out)
	return out
}

func (w *world) hasColumn(table, col string) bool {
	var ok bool
	_ = w.target.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_name = $1 AND column_name = $2)`, table, col).Scan(&ok)
	return ok
}

var addEmail = map[string]any{
	"name": "add email", "up_sql": "ALTER TABLE customers ADD COLUMN email text;\nCREATE INDEX customers_email_idx ON customers(email);",
	"down_sql": "DROP INDEX customers_email_idx;\nALTER TABLE customers DROP COLUMN email;",
}

func TestMigrationWorkflowApplyRollbackForward(t *testing.T) {
	w := newWorld(t)

	// View Only users cannot create, submit or execute.
	expectStatus(t, "viewer create", w.viewer.post(w.api("/migrations"), addEmail), http.StatusForbidden)
	mID := w.createMigration(w.editor, addEmail)
	expectStatus(t, "viewer submit", w.viewer.post(w.api("/migrations/%d/requests", mID), map[string]string{"action": "apply"}), http.StatusForbidden)

	// Draft -> Pending Review: one approval row per group with access.
	rID := w.submit(mID, model.ActionApply, 0)
	req := w.request(rID)
	if req.Status != model.ReqPendingReview || len(req.Approvals) != 2 {
		t.Fatalf("submitted request: %+v", req)
	}
	expectStatus(t, "second open request", w.editor.post(w.api("/migrations/%d/requests", mID), map[string]string{"action": "apply"}), http.StatusBadRequest)
	expectStatus(t, "edit while in review", w.editor.send("PUT", w.api("/migrations/%d", mID), addEmail), http.StatusBadRequest)
	expectStatus(t, "execute before approval", w.editor.post(w.api("/requests/%d/execute", rID), nil), http.StatusBadRequest)

	// Only leaders of the required groups approve.
	expectStatus(t, "member (not leader)", w.nina.post(w.api("/requests/%d/approve", rID), nil), http.StatusForbidden)
	expectStatus(t, "outsider", w.outsider.post(w.api("/requests/%d/approve", rID), nil), http.StatusForbidden)
	expectStatus(t, "admin without group", w.admin.post(w.api("/requests/%d/approve", rID), nil), http.StatusForbidden)
	expectStatus(t, "viewer leader", w.viewer.post(w.api("/requests/%d/approve", rID), nil), http.StatusOK)
	if s := w.request(rID).Status; s != model.ReqPendingReview {
		t.Fatalf("one of two approvals gave %s", s)
	}
	expectStatus(t, "approve twice", w.viewer.post(w.api("/requests/%d/approve", rID), nil), http.StatusForbidden)
	expectStatus(t, "editor leader", w.editor.post(w.api("/requests/%d/approve", rID), nil), http.StatusOK)
	if s := w.request(rID).Status; s != model.ReqApproved {
		t.Fatalf("all approvals gave %s", s)
	}

	// Execute returns a job ID at once; the worker runs it asynchronously.
	expectStatus(t, "viewer execute", w.viewer.post(w.api("/requests/%d/execute", rID), nil), http.StatusForbidden)
	jobID := w.execute(rID)
	if s := w.request(rID).Status; s != model.ReqQueued {
		t.Fatalf("after execute: %s", s)
	}
	expectStatus(t, "publish twice", w.editor.post(w.api("/requests/%d/execute", rID), nil), http.StatusBadRequest)
	if w.hasColumn("customers", "email") {
		t.Fatal("API ran the SQL itself")
	}
	r := w.viewer.get(w.api("/jobs/%d", jobID))
	expectStatus(t, "job", r, http.StatusOK)
	w.drain()

	if req = w.request(rID); req.Status != model.ReqSuccess {
		t.Fatalf("apply: %+v", req)
	}
	m := w.migration(mID)
	if m.Status != model.MigrationApplied || m.Version == nil || *m.Version != 1 || !w.hasColumn("customers", "email") {
		t.Fatalf("after apply: %+v", m)
	}
	// Executed SQL is immutable.
	expectStatus(t, "edit executed", w.editor.send("PUT", w.api("/migrations/%d", mID), addEmail), http.StatusBadRequest)
	expectStatus(t, "delete executed", w.editor.send("DELETE", w.api("/migrations/%d", mID), nil), http.StatusBadRequest)

	// Rollback runs the original SQL down as a new execution.
	if req = w.run(mID, model.ActionRollback, 0); req.Status != model.ReqSuccess || w.hasColumn("customers", "email") {
		t.Fatalf("rollback: %+v", req)
	}
	if m = w.migration(mID); m.Status != model.MigrationRolledBack {
		t.Fatalf("after rollback: %s", m.Status)
	}
	// Forward re-applies SQL up; the version stays.
	if req = w.run(mID, model.ActionForward, 0); req.Status != model.ReqSuccess || !w.hasColumn("customers", "email") {
		t.Fatalf("forward: %+v", req)
	}
	if m = w.migration(mID); m.Status != model.MigrationApplied || *m.Version != 1 {
		t.Fatalf("after forward: %+v", m)
	}

	full := w.history(false)
	if len(full) != 3 {
		t.Fatalf("history: %+v", full)
	}
	up, down, fwd := full[0], full[1], full[2]
	if down.Direction != model.DirDown || down.RevertsExecutionID == nil || *down.RevertsExecutionID != up.ID {
		t.Fatalf("rollback must reference the execution it undid: %+v", down)
	}
	if !strings.Contains(up.SQL, "ADD COLUMN email") || !strings.Contains(down.SQL, "DROP COLUMN email") || up.ExecutedBy != "eddie" {
		t.Fatalf("history must keep the SQL that ran: %+v / %+v", up, down)
	}
	// Hide rollback: E1 and its rollback E2 disappear, the forward stays.
	hidden := w.history(true)
	if len(hidden) != 1 || hidden[0].ID != fwd.ID || hidden[0].Action != model.ActionForward {
		t.Fatalf("hide rollback: %+v", hidden)
	}
	// History rows cannot be changed or deleted, even directly.
	if _, err := w.store.Pool().Exec(context.Background(), `UPDATE executions SET sql_text = 'x' WHERE id = $1`, up.ID); err == nil {
		t.Fatal("history row was overwritten")
	}
	if _, err := w.store.Pool().Exec(context.Background(), `DELETE FROM executions`); err == nil {
		t.Fatal("history rows deleted")
	}
}

func TestRejectAndCancel(t *testing.T) {
	w := newWorld(t)
	mID := w.createMigration(w.editor, addEmail)
	rID := w.submit(mID, model.ActionApply, 0)
	expectStatus(t, "reject", w.viewer.post(w.api("/requests/%d/reject", rID), map[string]string{"comment": "tunggu rilis aplikasi"}), http.StatusOK)
	if req := w.request(rID); req.Status != model.ReqRejected || req.Approvals[1].Comment != "tunggu rilis aplikasi" {
		t.Fatalf("rejected: %+v", req)
	}
	// After a rejection the draft is editable again, then a new request can be made.
	expectStatus(t, "edit after reject", w.editor.send("PUT", w.api("/migrations/%d", mID), addEmail), http.StatusOK)
	rID = w.submit(mID, model.ActionApply, 0)
	expectStatus(t, "viewer cancel", w.viewer.post(w.api("/requests/%d/cancel", rID), nil), http.StatusForbidden)
	expectStatus(t, "cancel", w.editor.post(w.api("/requests/%d/cancel", rID), nil), http.StatusOK)
	if s := w.request(rID).Status; s != model.ReqCancelled {
		t.Fatal(s)
	}
}

func TestRollbackRules(t *testing.T) {
	w := newWorld(t)
	a := w.createMigration(w.editor, map[string]any{"name": "A", "up_sql": "CREATE TABLE a (id int)", "down_sql": "DROP TABLE a"})
	b := w.createMigration(w.editor, map[string]any{"name": "B", "up_sql": "CREATE TABLE b (id int)", "down_sql": "DROP TABLE b"})
	c := w.createMigration(w.editor, map[string]any{"name": "C", "up_sql": "DELETE FROM orders WHERE total > 900",
		"irreversible": true, "irreversible_reason": "data yang dihapus tidak dapat dipulihkan"})
	for _, id := range []int64{a, b, c} {
		if req := w.run(id, model.ActionApply, 0); req.Status != model.ReqSuccess {
			t.Fatalf("apply %d: %+v", id, req)
		}
	}
	try := func(id int64, action, want string) {
		t.Helper()
		r := w.editor.post(w.api("/migrations/%d/requests", id), map[string]string{"action": action})
		expectStatus(t, action, r, http.StatusBadRequest)
		if !strings.Contains(r.body, want) {
			t.Fatalf("%s: %s", action, r.body)
		}
	}
	try(c, model.ActionRollback, "irreversible")   // C is irreversible
	try(a, model.ActionRollback, "aktif terakhir") // only the last active one

	// A -> B -> C (irreversible): add D, roll back D then B is blocked by C...
	d := w.createMigration(w.editor, map[string]any{"name": "D", "up_sql": "CREATE TABLE d (id int)", "down_sql": "DROP TABLE d"})
	e := w.createMigration(w.editor, map[string]any{"name": "E", "up_sql": "CREATE TABLE e (id int)", "down_sql": "DROP TABLE e"})
	w.run(d, model.ActionApply, 0)
	w.run(e, model.ActionApply, 0)
	try(d, model.ActionRollback, "aktif terakhir") // E must be rolled back before D
	w.run(e, model.ActionRollback, 0)
	w.run(d, model.ActionRollback, 0)
	try(e, model.ActionForward, "urutan linear") // D (lower version) first
	if req := w.run(d, model.ActionForward, 0); req.Status != model.ReqSuccess {
		t.Fatalf("forward D: %+v", req)
	}
	// Forward does not run the next migration automatically.
	if w.migration(e).Status != model.MigrationRolledBack {
		t.Fatal("E forwarded automatically")
	}
	var versions []int
	for _, id := range []int64{a, b, c, d, e} {
		versions = append(versions, *w.migration(id).Version)
	}
	if fmt.Sprint(versions) != "[1 2 3 4 5]" {
		t.Fatalf("versions: %v", versions)
	}
}

func TestFailedMigrationIsRolledBackAndRecorded(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	mID := w.createMigration(w.editor, map[string]any{"name": "broken",
		"up_sql":   "CREATE TABLE half_done (id int);\nINSERT INTO customers (name) VALUES ('x');\nINSERT INTO missing_table VALUES (1);",
		"down_sql": "DROP TABLE half_done"})
	req := w.run(mID, model.ActionApply, 0)
	if req.Status != model.ReqFailed || !strings.Contains(req.Error, "missing_table") {
		t.Fatalf("failed request: %+v", req)
	}
	var exists bool
	var customers int
	_ = w.target.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'half_done')`).Scan(&exists)
	_ = w.target.QueryRow(ctx, `SELECT count(*) FROM customers`).Scan(&customers)
	if exists || customers != 75 {
		t.Fatalf("partial changes kept: table=%v customers=%d", exists, customers)
	}
	if m := w.migration(mID); m.Status != model.MigrationDraft || m.Version != nil || !m.Locked {
		t.Fatalf("failed migration: %+v", m)
	}
	h := w.history(false)
	if len(h) != 1 || h[0].Status != model.ExecFailed || !strings.Contains(h[0].Error, "SQLSTATE 42P01") || !strings.Contains(h[0].SQL, "missing_table") {
		t.Fatalf("failed attempt must be in the history: %+v", h)
	}
	// The failed attempt does not hold the database: other migrations still run.
	other := w.createMigration(w.editor, map[string]any{"name": "ok", "up_sql": "CREATE TABLE fine (id int)", "down_sql": "DROP TABLE fine"})
	if r := w.run(other, model.ActionApply, 0); r.Status != model.ReqSuccess || *w.migration(other).Version != 1 {
		t.Fatalf("next migration: %+v", r)
	}
}

func TestValidateDryRunDoesNotChangeData(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	mID := w.createMigration(w.editor, map[string]any{"name": "bulk",
		"up_sql":   "ALTER TABLE customers ADD COLUMN vip boolean;\nUPDATE customers SET city = 'Jakarta' WHERE id < 10;",
		"down_sql": "UPDATE customers SET city = 'Bandung' WHERE id < 10;\nALTER TABLE customers DROP COLUMN vip;"})
	r := w.editor.post(w.api("/migrations/%d/validate", mID), map[string]bool{"analyze": true})
	expectStatus(t, "validate", r, http.StatusOK)
	for _, s := range []string{`"valid":true`, "actual time", "Update on public.customers", "tidak dapat di-EXPLAIN"} {
		if !strings.Contains(r.body, s) {
			t.Errorf("validation output misses %q", s)
		}
	}
	var jakarta int
	_ = w.target.QueryRow(ctx, `SELECT count(*) FROM customers WHERE city = 'Jakarta'`).Scan(&jakarta)
	if jakarta != 0 || w.hasColumn("customers", "vip") {
		t.Fatal("dry run changes were not rolled back")
	}
	expectStatus(t, "viewer validate", w.viewer.post(w.api("/migrations/%d/validate", mID), map[string]bool{}), http.StatusForbidden)

	// Invalid definitions are rejected before they are stored.
	bad := map[string]any{"name": "tx", "up_sql": "BEGIN; SELECT 1; COMMIT;", "down_sql": "SELECT 1", "mode": "normal"}
	expectStatus(t, "manual tx", w.editor.post(w.api("/migrations"), bad), http.StatusBadRequest)
	bad = map[string]any{"name": "no down", "up_sql": "SELECT 1", "mode": "normal"}
	expectStatus(t, "no down and not irreversible", w.editor.post(w.api("/migrations"), bad), http.StatusBadRequest)
	bad = map[string]any{"name": "batch", "up_sql": "UPDATE orders SET total = 1", "down_sql": "SELECT 1", "mode": "batch",
		"batch_table": "orders", "batch_column": "id", "batch_size": 10}
	expectStatus(t, "batch without $1/$2", w.editor.post(w.api("/migrations"), bad), http.StatusBadRequest)
}

func (w *world) seedBig(rows int) {
	w.t.Helper()
	_, err := w.target.Exec(context.Background(), fmt.Sprintf(`CREATE TABLE big (id bigint PRIMARY KEY, val int NOT NULL);
		INSERT INTO big SELECT g, g FROM generate_series(1, %d) g;`, rows))
	if err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) sumBig() (sum int64) {
	_ = w.target.QueryRow(context.Background(), `SELECT sum(val) FROM big`).Scan(&sum)
	return sum
}

func batchMigration(name, up, down string) map[string]any {
	return map[string]any{"name": name, "mode": "batch", "batch_table": "big", "batch_column": "id", "batch_size": 100,
		"up_sql": up, "down_sql": down}
}

func TestBatchMigration(t *testing.T) {
	w := newWorld(t)
	w.seedBig(1000)
	base := w.sumBig()
	mID := w.createMigration(w.editor, batchMigration("bump",
		"UPDATE big SET val = val + 1 WHERE id > $1 AND id <= $2", "UPDATE big SET val = val - 1 WHERE id > $1 AND id <= $2"))
	req := w.run(mID, model.ActionApply, 0)
	if req.Status != model.ReqSuccess || req.BatchesDone != 10 || req.RowsDone != 1000 || req.ScopeMax == nil || *req.ScopeMax != 1000 {
		t.Fatalf("batch apply: %+v", req)
	}
	if w.sumBig() != base+1000 {
		t.Fatal("every row must be updated exactly once")
	}
	var checkpoints int
	_ = w.target.QueryRow(context.Background(), `SELECT count(*) FROM dbmp_checkpoints WHERE series_id = $1`, req.SeriesID).Scan(&checkpoints)
	if checkpoints != 10 {
		t.Fatalf("checkpoints: %d", checkpoints)
	}
	// Rollback undoes the committed batches in reverse order.
	if r := w.run(mID, model.ActionRollback, 0); r.Status != model.ReqSuccess || r.BatchesDone != 10 || w.sumBig() != base {
		t.Fatalf("batch rollback: %+v sum=%d", r, w.sumBig())
	}
}

func TestPartialBatchHoldsDatabaseThenResume(t *testing.T) {
	w := newWorld(t)
	w.seedBig(1000)
	if _, err := w.target.Exec(context.Background(), `ALTER TABLE big ADD CONSTRAINT val_max CHECK (val <= 1500)`); err != nil {
		t.Fatal(err)
	}
	mID := w.createMigration(w.editor, batchMigration("plus 1000",
		"UPDATE big SET val = val + 1000 WHERE id > $1 AND id <= $2", "UPDATE big SET val = val - 1000 WHERE id > $1 AND id <= $2"))
	req := w.run(mID, model.ActionApply, 0)
	// ids 1..500 pass the check; batch 6 (ids 501-600) violates it.
	if req.Status != model.ReqPartial || req.BatchesDone != 5 || req.RowsDone != 500 || !strings.Contains(req.Error, "23514") {
		t.Fatalf("partial run: %+v", req)
	}
	if m := w.migration(mID); m.Status != model.MigrationPartial || m.Version == nil {
		t.Fatalf("partial migration: %+v", m)
	}
	h := w.history(false)
	if len(h) != 1 || h[0].Status != model.ExecPartial || h[0].Batches != 5 {
		t.Fatalf("partial attempt in history: %+v", h)
	}

	// The partial run holds every other migration on the database.
	other := w.createMigration(w.editor, map[string]any{"name": "other", "up_sql": "CREATE TABLE x (id int)", "down_sql": "DROP TABLE x"})
	r := w.editor.post(w.api("/migrations/%d/requests", other), map[string]string{"action": "apply"})
	expectStatus(t, "held", r, http.StatusConflict)

	// Fix the cause, then resume from the checkpoint: batches 1-5 are not repeated.
	if _, err := w.target.Exec(context.Background(), `ALTER TABLE big DROP CONSTRAINT val_max`); err != nil {
		t.Fatal(err)
	}
	res := w.run(mID, model.ActionResume, req.ID)
	if res.Status != model.ReqSuccess || res.BatchesDone != 10 || res.RowsDone != 1000 || res.SeriesID != req.SeriesID {
		t.Fatalf("resume: %+v", res)
	}
	var wrong int
	_ = w.target.QueryRow(context.Background(), `SELECT count(*) FROM big WHERE val <> id + 1000`).Scan(&wrong)
	if wrong != 0 {
		t.Fatalf("%d rows changed twice or not at all", wrong)
	}
	if s := w.request(req.ID).Status; s != model.ReqSuperseded {
		t.Fatalf("stopped request: %s", s)
	}
	if r := w.run(other, model.ActionApply, 0); r.Status != model.ReqSuccess {
		t.Fatalf("database still held: %+v", r)
	}
}

func TestCancelCommittedBatches(t *testing.T) {
	w := newWorld(t)
	w.seedBig(1000)
	base := w.sumBig()
	if _, err := w.target.Exec(context.Background(), `ALTER TABLE big ADD CONSTRAINT val_max CHECK (val <= 1500)`); err != nil {
		t.Fatal(err)
	}
	mID := w.createMigration(w.editor, batchMigration("plus 1000",
		"UPDATE big SET val = val + 1000 WHERE id > $1 AND id <= $2", "UPDATE big SET val = val - 1000 WHERE id > $1 AND id <= $2"))
	req := w.run(mID, model.ActionApply, 0)
	if req.Status != model.ReqPartial {
		t.Fatalf("expected partial: %+v", req)
	}
	// Cancel = compensate the committed batches with the approved SQL down.
	res := w.run(mID, model.ActionRollback, 0)
	if res.Status != model.ReqSuccess || res.BatchesDone != 5 || w.sumBig() != base {
		t.Fatalf("cancel: %+v sum=%d want %d", res, w.sumBig(), base)
	}
	if m := w.migration(mID); m.Status != model.MigrationRolledBack {
		t.Fatalf("after cancel: %s", m.Status)
	}
}

// TestCommitEvidencePreventsDoubleCommit simulates a worker that committed in
// the target database and crashed before recording it: the retry finds the
// checkpoint and does not run the SQL again.
func TestCommitEvidencePreventsDoubleCommit(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	mID := w.createMigration(w.editor, map[string]any{"name": "insert", "up_sql": "INSERT INTO customers (name) VALUES ('once')",
		"down_sql": "DELETE FROM customers WHERE name = 'once'"})
	rID := w.submit(mID, model.ActionApply, 0)
	w.approveAll(rID)
	w.execute(rID)
	m := w.migration(mID)

	// "Previous attempt": committed in the target, then the worker died.
	if err := w.targets.EnsureCheckpoints(ctx, w.dbID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.targets.RunOnce(ctx, w.dbID, rID, 0, model.DirUp, m.UpSQL); err != nil {
		t.Fatal(err)
	}
	// A claimed job with a stale heartbeat is recovered as uncertain.
	job, err := w.store.ClaimJob(ctx, "dead-worker")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.store.Pool().Exec(ctx, `UPDATE jobs SET heartbeat_at = now() - interval '1 hour' WHERE id = $1`, job.ID)
	if ids, err := w.store.RecoverStale(ctx, 30*time.Second); err != nil || len(ids) != 1 {
		t.Fatalf("recover: %v %v", ids, err)
	}
	if s := w.request(rID).Status; s != model.ReqUncertain {
		t.Fatalf("recovered request: %s", s)
	}
	// While uncertain, other migrations on the database wait.
	other := w.createMigration(w.editor, map[string]any{"name": "other", "up_sql": "CREATE TABLE y (id int)", "down_sql": "DROP TABLE y"})
	expectStatus(t, "held while uncertain", w.editor.post(w.api("/migrations/%d/requests", other), map[string]string{"action": "apply"}), http.StatusConflict)

	w.drain()
	if s := w.request(rID).Status; s != model.ReqSuccess {
		t.Fatalf("after retry: %s", s)
	}
	var n int
	_ = w.target.QueryRow(ctx, `SELECT count(*) FROM customers WHERE name = 'once'`).Scan(&n)
	if n != 1 {
		t.Fatalf("committed %d times", n)
	}
}

func TestRevalidationRequiresApprovalOfNewGroups(t *testing.T) {
	w := newWorld(t)
	mID := w.createMigration(w.editor, addEmail)
	rID := w.submit(mID, model.ActionApply, 0)
	w.approveAll(rID)
	w.execute(rID)
	// A new group gets access after the approvals: its leader never saw the change.
	g := w.admin.post("/api/admin/groups", map[string]string{"name": "auditors"}).id(t)
	w.admin.send("PUT", fmt.Sprintf("/api/admin/groups/%d/grants", g), map[string]any{"database_id": w.dbID, "level": "view"})
	w.drain()
	req := w.request(rID)
	if req.Status != model.ReqFailed || !strings.Contains(req.Error, "auditors") || w.hasColumn("customers", "email") {
		t.Fatalf("revalidation: %+v", req)
	}
}

// TestWorkersRunOneMigrationPerDatabase runs several workers in parallel on
// jobs of one database: executions never overlap and versions stay linear.
func TestWorkersRunOneMigrationPerDatabase(t *testing.T) {
	w := newWorld(t)
	const n = 4
	var reqs []int64
	for i := 0; i < n; i++ {
		id := w.createMigration(w.editor, map[string]any{"name": fmt.Sprintf("t%d", i),
			"up_sql": fmt.Sprintf("SELECT pg_sleep(0.2); CREATE TABLE t%d (id int)", i), "down_sql": fmt.Sprintf("DROP TABLE t%d", i)})
		r := w.submit(id, model.ActionApply, 0)
		w.approveAll(r)
		reqs = append(reqs, r)
	}
	for _, r := range reqs {
		w.execute(r)
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			wk := w.worker(fmt.Sprintf("w%d", i))
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				done := true
				for _, r := range reqs {
					if s := w.request(r).Status; s != model.ReqSuccess {
						done = false
					}
				}
				if done {
					return
				}
				if found, _ := wk.RunOnce(context.Background()); !found {
					time.Sleep(20 * time.Millisecond)
				}
			}
		}(i)
	}
	wg.Wait()
	h := w.history(false)
	if len(h) != n {
		t.Fatalf("history: %d executions", len(h))
	}
	for i := 1; i < len(h); i++ {
		if h[i].StartedAt.Before(*h[i-1].FinishedAt) {
			t.Fatalf("executions %d and %d overlap on one database", h[i-1].ID, h[i].ID)
		}
	}
	ms, _ := w.store.ListMigrations(context.Background(), w.dbID)
	for i, m := range ms {
		if m.Version == nil || *m.Version != i+1 {
			t.Fatalf("versions not linear: %d -> %v", i, m.Version)
		}
	}
}
