package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"dbmp/internal/authz"
	"dbmp/internal/model"
	"dbmp/internal/targetdb"
)

type migrationListData struct {
	dbHeader
	Migrations []model.Migration
}

func (s *Server) migrationList(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionView)
	if !ok {
		return
	}
	ms, err := s.Store.ListMigrations(r.Context(), h.DB.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, http.StatusOK, "migrations", s.page(w, r, "Migration — "+h.DB.Name, migrationListData{h, ms}))
}

type migrationData struct {
	dbHeader
	Migration model.Migration
	Explain   []model.ExplainResult
	Analyze   bool
}

func (s *Server) migrationNew(w http.ResponseWriter, r *http.Request) {
	if h, ok := s.loadDB(w, r, authz.ActionEdit); ok {
		s.render(w, http.StatusOK, "migration", s.page(w, r, "Migration baru", migrationData{dbHeader: h}))
	}
}

func (s *Server) formMigration(w http.ResponseWriter, r *http.Request, d migrationData, err error) {
	var ve *model.ValidationError
	if !errors.As(err, &ve) {
		s.fail(w, r, err)
		return
	}
	if d.Migration.Status != model.MigrationApplied {
		d.Migration.Name, d.Migration.SQL = r.PostFormValue("name"), r.PostFormValue("sql")
	}
	p := s.page(w, r, "Migration", d)
	p.Error = ve.Msg
	s.render(w, http.StatusBadRequest, "migration", p)
}

func (s *Server) migrationCreate(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionEdit)
	if !ok {
		return
	}
	name, sql := strings.TrimSpace(r.PostFormValue("name")), r.PostFormValue("sql")
	if err := targetdb.Validate(name, sql); err != nil {
		s.formMigration(w, r, migrationData{dbHeader: h}, err)
		return
	}
	id, err := s.Store.CreateMigration(r.Context(), h.DB.ID, name, sql, principal(r).Username)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.done(w, r, nil, "Draft migration tersimpan. Tinjau SQL lalu jalankan bila sudah siap.", fmt.Sprintf("/db/%d/migrations/%d", h.DB.ID, id))
}

func (s *Server) loadMigration(w http.ResponseWriter, r *http.Request, action authz.Action) (migrationData, bool) {
	h, ok := s.loadDB(w, r, action)
	if !ok {
		return migrationData{}, false
	}
	m, err := s.Store.GetMigration(r.Context(), h.DB.ID, pathID(r, "mID"))
	if err != nil {
		s.fail(w, r, err)
		return migrationData{}, false
	}
	return migrationData{dbHeader: h, Migration: m}, true
}

func (d migrationData) url() string {
	return fmt.Sprintf("/db/%d/migrations/%d", d.DB.ID, d.Migration.ID)
}

func (s *Server) migrationDetail(w http.ResponseWriter, r *http.Request) {
	if d, ok := s.loadMigration(w, r, authz.ActionView); ok {
		s.render(w, http.StatusOK, "migration", s.page(w, r, d.Migration.Name, d))
	}
}

func (s *Server) migrationUpdate(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadMigration(w, r, authz.ActionEdit)
	if !ok {
		return
	}
	name, sql := strings.TrimSpace(r.PostFormValue("name")), r.PostFormValue("sql")
	err := targetdb.Validate(name, sql)
	if err == nil {
		err = s.Store.UpdateMigration(r.Context(), d.DB.ID, d.Migration.ID, name, sql)
	}
	if errors.Is(err, model.ErrNotFound) {
		err = model.Invalid("Migration yang sudah diterapkan tidak dapat diubah.")
	}
	if err != nil {
		s.formMigration(w, r, d, err)
		return
	}
	s.done(w, r, nil, "Perubahan migration tersimpan.", d.url())
}

func (s *Server) migrationExecute(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadMigration(w, r, authz.ActionEdit)
	if !ok {
		return
	}
	m, err := s.Store.RunMigration(r.Context(), d.DB.ID, d.Migration.ID, principal(r).Username, func(sql string) (string, error) {
		if err := targetdb.Validate(d.Migration.Name, sql); err != nil {
			return "", err
		}
		return s.Targets.Exec(r.Context(), d.DB.ID, sql)
	})
	switch {
	case err != nil:
		s.done(w, r, err, "", d.url())
	case m.Status == model.MigrationApplied:
		s.done(w, r, nil, fmt.Sprintf("Migration berhasil di-commit sebagai versi %d.", *m.Version), d.url())
	default:
		s.done(w, r, model.Invalid("Migration gagal dan seluruh perubahan di-rollback: "+m.Error), "", d.url())
	}
}

func (s *Server) migrationExplain(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadMigration(w, r, authz.ActionEdit)
	if !ok {
		return
	}
	d.Analyze = r.PostFormValue("analyze") == "1"
	res, err := s.Targets.Explain(r.Context(), d.DB.ID, targetdb.Split(d.Migration.SQL), d.Analyze)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Explain = res
	s.render(w, http.StatusOK, "migration", s.page(w, r, d.Migration.Name, d))
}

func (s *Server) migrationDelete(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadMigration(w, r, authz.ActionEdit)
	if !ok {
		return
	}
	if err := s.Store.DeleteMigration(r.Context(), d.DB.ID, d.Migration.ID); err != nil {
		s.done(w, r, model.Invalid("Migration yang sudah diterapkan tidak dapat dihapus."), "", d.url())
		return
	}
	s.done(w, r, nil, "Migration dihapus.", fmt.Sprintf("/db/%d/migrations", d.DB.ID))
}
