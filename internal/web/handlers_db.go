package web

import (
	"net/http"
	"strconv"

	"dbmp/internal/authz"
	"dbmp/internal/model"
)

type dbHeader struct {
	DB    model.Database
	Level authz.Level
}

func (s *Server) loadDB(w http.ResponseWriter, r *http.Request, action authz.Action) (dbHeader, bool) {
	id := pathID(r, "dbID")
	lvl, err := s.Policy.Level(r.Context(), principal(r), id)
	if err == nil && !authz.Allows(lvl, action) {
		err = authz.ErrForbidden
	}
	if err != nil {
		s.fail(w, r, err)
		return dbHeader{}, false
	}
	db, err := s.Store.GetDatabase(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return dbHeader{}, false
	}
	return dbHeader{DB: db, Level: lvl}, true
}

func (s *Server) dbInfo(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionView)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": h.DB.ID, "name": h.DB.Name, "level": h.Level, "can_edit": authz.Allows(h.Level, authz.ActionEdit),
	})
}

func (s *Server) dbTables(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionView)
	if !ok {
		return
	}
	tables, err := s.Targets.ListTables(r.Context(), h.DB.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if tables == nil {
		tables = []model.TableInfo{}
	}
	writeJSON(w, http.StatusOK, tables)
}

func (s *Server) dbTable(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionView)
	if !ok {
		return
	}
	detail, err := s.Targets.Table(r.Context(), h.DB.ID, r.PathValue("schema"), r.PathValue("table"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

const pageSize = 50

func (s *Server) dbRows(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionView)
	if !ok {
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	page, err := s.Targets.Rows(r.Context(), h.DB.ID, r.PathValue("schema"), r.PathValue("table"), pageSize, max(offset, 0))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}
