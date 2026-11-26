package workflow

import (
	"errors"
	"strings"
	"testing"

	"dbmp/internal/model"
)

func v(n int) *int { return &n }

func mig(id int64, status string, version *int) model.Migration {
	return model.Migration{ID: id, Name: "m", Status: status, Version: version, Mode: model.ModeNormal, DownSQL: "x"}
}

func TestCheck(t *testing.T) {
	a := mig(1, model.MigrationApplied, v(1))
	b := mig(2, model.MigrationApplied, v(2))
	draft := mig(3, model.MigrationDraft, nil)
	st := State{Migrations: []model.Migration{a, b, draft}, Requests: []model.Request{
		{ID: 10, MigrationID: 1, Direction: model.DirUp, Status: model.ReqSuccess, SeriesID: 10},
		{ID: 11, MigrationID: 2, Direction: model.DirUp, Status: model.ReqSuccess, SeriesID: 11},
	}}

	cases := []struct {
		name    string
		st      State
		m       model.Migration
		action  string
		target  int64
		wantErr string
	}{
		{"apply draft", st, draft, model.ActionApply, 0, ""},
		{"apply applied", st, a, model.ActionApply, 0, "draft"},
		{"rollback last active", st, b, model.ActionRollback, 0, ""},
		{"rollback not last", st, a, model.ActionRollback, 0, "aktif terakhir"},
		{"rollback irreversible", st, func() model.Migration { m := b; m.DownSQL, m.IrreversibleReason = "", "hapus data"; return m }(),
			model.ActionRollback, 0, "irreversible"},
		{"forward applied", st, b, model.ActionForward, 0, "sudah di-rollback"},
		{"unknown", st, b, "drop", 0, "tidak dikenal"},
	}
	for _, c := range cases {
		_, err := Check(c.st, c.m, c.action, c.target, 0)
		if c.wantErr == "" && err != nil || c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.wantErr)
		}
	}

	p, err := Check(st, b, model.ActionRollback, 0, 0)
	if err != nil || p.Direction != model.DirDown || p.UndoSeriesID == nil || *p.UndoSeriesID != 11 {
		t.Fatalf("rollback plan: %+v %v", p, err)
	}
}

func TestForwardFollowsLinearOrder(t *testing.T) {
	st := State{Migrations: []model.Migration{
		mig(1, model.MigrationApplied, v(1)), mig(2, model.MigrationRolledBack, v(2)), mig(3, model.MigrationRolledBack, v(3)),
	}}
	if _, err := Check(st, st.Migrations[2], model.ActionForward, 0, 0); err == nil || !strings.Contains(err.Error(), "versi 2") {
		t.Fatalf("forward v3 before v2: %v", err)
	}
	if _, err := Check(st, st.Migrations[1], model.ActionForward, 0, 0); err != nil {
		t.Fatalf("forward v2: %v", err)
	}
	st.Migrations = append(st.Migrations, mig(4, model.MigrationApplied, v(4)))
	if _, err := Check(st, st.Migrations[1], model.ActionForward, 0, 0); err == nil {
		t.Fatal("forward below the active top accepted")
	}
}

func TestPartialRunHoldsTheDatabase(t *testing.T) {
	part := mig(1, model.MigrationPartial, v(1))
	part.Mode = model.ModeBatch
	draft := mig(2, model.MigrationDraft, nil)
	stopped := model.Request{ID: 20, MigrationID: 1, Direction: model.DirUp, Status: model.ReqPartial, SeriesID: 20}
	st := State{Migrations: []model.Migration{part, draft}, Requests: []model.Request{stopped}}

	_, err := Check(st, draft, model.ActionApply, 0, 0)
	var he *HeldError
	if !errors.As(err, &he) {
		t.Fatalf("apply while partial: %v", err)
	}
	p, err := Check(st, part, model.ActionResume, 20, 0)
	if err != nil || p.SeriesID != 20 || p.Direction != model.DirUp || *p.TargetRequestID != 20 {
		t.Fatalf("resume: %+v %v", p, err)
	}
	p, err = Check(st, part, model.ActionRollback, 0, 0)
	if err != nil || *p.UndoSeriesID != 20 {
		t.Fatalf("cancel batches: %+v %v", p, err)
	}
	st.Requests[0].Status = model.ReqUncertain
	if _, err := Check(st, draft, model.ActionApply, 0, 20); err != nil {
		t.Fatalf("self excluded: %v", err)
	}
}

func TestOneOpenRequestPerMigration(t *testing.T) {
	draft := mig(1, model.MigrationDraft, nil)
	st := State{Migrations: []model.Migration{draft}, Requests: []model.Request{{ID: 5, MigrationID: 1, Status: model.ReqApproved}}}
	if _, err := Check(st, draft, model.ActionApply, 0, 0); err == nil {
		t.Fatal("second open request accepted")
	}
	if _, err := Check(st, draft, model.ActionApply, 0, 5); err != nil {
		t.Fatalf("revalidating the open request itself: %v", err)
	}
	if got := Actions(State{Migrations: []model.Migration{draft}}, draft); len(got) != 1 || got[0] != model.ActionApply {
		t.Fatalf("actions: %v", got)
	}
}

func TestHideRollbacks(t *testing.T) {
	id := func(n int64) *int64 { return &n }
	execs := []model.Execution{
		{ID: 1, Direction: model.DirUp, Status: model.ExecSuccess},
		{ID: 2, Direction: model.DirUp, Status: model.ExecSuccess},
		{ID: 3, Direction: model.DirDown, Status: model.ExecSuccess, RevertsExecutionID: id(2)},
		{ID: 4, Direction: model.DirUp, Status: model.ExecSuccess},
		{ID: 5, Direction: model.DirUp, Status: model.ExecSuccess},
		{ID: 6, Direction: model.DirDown, Status: model.ExecFailed},
	}
	var got []int64
	for _, e := range HideRollbacks(execs) {
		got = append(got, e.ID)
	}
	if want := []int64{1, 4, 5, 6}; len(got) != len(want) || got[0] != 1 || got[1] != 4 || got[2] != 5 || got[3] != 6 {
		t.Fatalf("visible = %v, want %v", got, want)
	}
	if len(execs) != 6 {
		t.Fatal("filter changed the input")
	}
}

func TestFingerprint(t *testing.T) {
	m := mig(1, model.MigrationDraft, nil)
	m.UpSQL = "CREATE TABLE a (id int)"
	f := Fingerprint(m)
	m.DownSQL = "DROP TABLE a"
	if Fingerprint(m) == f {
		t.Fatal("changed down SQL kept the fingerprint")
	}
}
