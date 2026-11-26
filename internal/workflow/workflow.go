package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"dbmp/internal/model"
)

type State struct {
	Migrations []model.Migration
	Requests   []model.Request
}

type Plan struct {
	Direction       string
	TargetRequestID *int64
	SeriesID        int64
	UndoSeriesID    *int64
}

func Check(st State, m model.Migration, action string, targetID, self int64) (Plan, error) {
	for _, r := range st.Requests {
		if r.MigrationID == m.ID && r.ID != self && model.Open(r.Status) {
			return Plan{}, model.Invalid(fmt.Sprintf("Migration ini masih memiliki pengajuan #%d yang sedang diproses.", r.ID))
		}
	}
	blocking := blockingRequests(st, self)

	switch action {
	case model.ActionApply:
		if err := held(blocking); err != nil {
			return Plan{}, err
		}
		if m.Status != model.MigrationDraft {
			return Plan{}, model.Invalid("Hanya migration draft yang dapat di-apply.")
		}
		return Plan{Direction: model.DirUp}, nil

	case model.ActionForward:
		if err := held(blocking); err != nil {
			return Plan{}, err
		}
		if m.Status != model.MigrationRolledBack {
			return Plan{}, model.Invalid("Forward hanya untuk migration yang sudah di-rollback.")
		}
		next := nextForward(st.Migrations)
		if next == nil {
			return Plan{}, model.Invalid("Migration ini berada di bawah migration aktif terakhir. Buat migration baru untuk menerapkan ulang perubahan.")
		}
		if next.ID != m.ID {
			return Plan{}, model.Invalid(fmt.Sprintf("Forward mengikuti urutan linear: forward versi %d (%s) terlebih dahulu.", *next.Version, next.Name))
		}
		return Plan{Direction: model.DirUp}, nil

	case model.ActionRollback:
		if m.IrreversibleReason != "" {
			return Plan{}, model.Invalid("Migration irreversible tidak dapat di-rollback: " + m.IrreversibleReason)
		}
		if m.DownSQL == "" {
			return Plan{}, model.Invalid("Migration ini tidak memiliki SQL down.")
		}
		top := topActive(st.Migrations)
		if top == nil || top.ID != m.ID {
			if top == nil {
				return Plan{}, model.Invalid("Tidak ada migration aktif untuk di-rollback.")
			}
			return Plan{}, model.Invalid(fmt.Sprintf("Rollback hanya untuk migration aktif terakhir: versi %d (%s).", *top.Version, top.Name))
		}
		for _, b := range blocking {
			if !(b.MigrationID == m.ID && b.Status == model.ReqPartial && b.Direction == model.DirUp) {
				return Plan{}, held([]model.Request{b})
			}
		}
		p := Plan{Direction: model.DirDown}
		if up := lastUp(st.Requests, m.ID); up != nil {
			p.TargetRequestID, p.UndoSeriesID = &up.ID, &up.SeriesID
		} else if m.Mode == model.ModeBatch {
			return Plan{}, model.Invalid("Riwayat batch migration ini tidak ditemukan; rollback per batch tidak aman.")
		}
		return p, nil

	case model.ActionResume:
		for _, r := range st.Requests {
			if r.ID == targetID && r.MigrationID == m.ID {
				if !model.Blocking(r.Status) {
					return Plan{}, model.Invalid("Hanya pengajuan berstatus partial, partially_reverted, atau uncertain yang dapat dilanjutkan.")
				}
				return Plan{Direction: r.Direction, TargetRequestID: &r.ID, SeriesID: r.SeriesID, UndoSeriesID: r.UndoSeriesID}, nil
			}
		}
		return Plan{}, model.Invalid("Pengajuan yang ingin dilanjutkan tidak ditemukan.")
	}
	return Plan{}, model.Invalid("Aksi tidak dikenal.")
}

func blockingRequests(st State, self int64) []model.Request {
	var out []model.Request
	for _, r := range st.Requests {
		if r.ID != self && model.Blocking(r.Status) {
			out = append(out, r)
		}
	}
	return out
}

type HeldError struct{ Msg string }

func (e *HeldError) Error() string { return e.Msg }

func held(blocking []model.Request) error {
	if len(blocking) == 0 {
		return nil
	}
	b := blocking[0]
	return &HeldError{Msg: fmt.Sprintf("Database ditahan: pengajuan #%d (%s) berstatus %s. Lanjutkan (resume) atau batalkan (rollback) dulu.",
		b.ID, b.MigrationName, b.Status)}
}

func Fingerprint(m model.Migration) string {
	h := sha256.New()
	for _, part := range []string{m.UpSQL, m.DownSQL, m.IrreversibleReason, m.Mode, m.BatchTable, m.BatchColumn, strconv.Itoa(m.BatchSize)} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func topActive(ms []model.Migration) *model.Migration {
	var top *model.Migration
	for i, m := range ms {
		if (m.Status == model.MigrationApplied || m.Status == model.MigrationPartial) && m.Version != nil &&
			(top == nil || *m.Version > *top.Version) {
			top = &ms[i]
		}
	}
	return top
}

func nextForward(ms []model.Migration) *model.Migration {
	floor := 0
	if top := topActive(ms); top != nil {
		floor = *top.Version
	}
	var next *model.Migration
	for i, m := range ms {
		if m.Status == model.MigrationRolledBack && m.Version != nil && *m.Version > floor &&
			(next == nil || *m.Version < *next.Version) {
			next = &ms[i]
		}
	}
	return next
}

func lastUp(rs []model.Request, migrationID int64) *model.Request {
	var last *model.Request
	for i, r := range rs {
		if r.MigrationID == migrationID && r.Direction == model.DirUp &&
			(r.Status == model.ReqSuccess || r.Status == model.ReqPartial) && (last == nil || r.ID > last.ID) {
			last = &rs[i]
		}
	}
	return last
}

func Actions(st State, m model.Migration) []string {
	var out []string
	for _, a := range []string{model.ActionApply, model.ActionRollback, model.ActionForward} {
		if _, err := Check(st, m, a, 0, 0); err == nil {
			out = append(out, a)
		}
	}
	return out
}

func HideRollbacks(execs []model.Execution) []model.Execution {
	hidden := map[int64]bool{}
	for _, e := range execs {
		if e.Direction == model.DirDown && e.Status == model.ExecSuccess && e.RevertsExecutionID != nil {
			hidden[e.ID], hidden[*e.RevertsExecutionID] = true, true
		}
	}
	out := []model.Execution{}
	for _, e := range execs {
		if !hidden[e.ID] {
			out = append(out, e)
		}
	}
	return out
}
