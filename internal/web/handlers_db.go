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

type tablesData struct {
	dbHeader
	Tables []model.TableInfo
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
	s.render(w, http.StatusOK, "db_tables", s.page(w, r, h.DB.Name, tablesData{h, tables}))
}

type tableData struct {
	dbHeader
	Table model.TableDetail
	Tab   string
	Rows  model.RowPage
}

const pageSize = 50

func (s *Server) dbTable(w http.ResponseWriter, r *http.Request) {
	h, ok := s.loadDB(w, r, authz.ActionView)
	if !ok {
		return
	}
	schema, table := r.PathValue("schema"), r.PathValue("table")
	detail, err := s.Targets.Table(r.Context(), h.DB.ID, schema, table)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := tableData{dbHeader: h, Table: detail, Tab: "structure"}
	if r.URL.Query().Get("tab") == "data" {
		d.Tab = "data"
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if d.Rows, err = s.Targets.Rows(r.Context(), h.DB.ID, schema, table, pageSize, max(offset, 0)); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.render(w, http.StatusOK, "db_table", s.page(w, r, schema+"."+table, d))
}
