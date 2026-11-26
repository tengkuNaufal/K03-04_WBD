package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"dbmp/internal/auth"
	"dbmp/internal/authz"
	"dbmp/internal/model"
	"dbmp/internal/targetdb"
)

// audit writes an activity log line for an admin action.
func audit(r *http.Request, event string, attrs ...any) {
	attrs = append([]any{"event", event, "actor", principal(r).Username, "status", "ok"}, attrs...)
	slog.InfoContext(r.Context(), "admin action", attrs...)
}

// done answers an admin action: the message on success, or the error.
func (s *Server) done(w http.ResponseWriter, r *http.Request, err error, msg string) {
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeOK(w, msg)
}

// ---- users ----------------------------------------------------------------------

func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.ListUsers(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) adminUserCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username, Password string
		IsAdmin            bool `json:"is_admin"`
	}
	err := decode(r, &in)
	username := auth.NormalizeUsername(in.Username)
	var hash string
	var id int64
	if err == nil {
		err = auth.ValidateUsername(username)
	}
	if err == nil {
		hash, err = auth.HashPassword(in.Password)
	}
	if err == nil {
		id, err = s.Store.CreateUser(r.Context(), username, hash, in.IsAdmin)
	}
	if err = invalid(err); err != nil {
		s.fail(w, r, err)
		return
	}
	audit(r, "admin.user.create", "user_id", id)
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) adminUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.Store.GetUserByID(r.Context(), pathID(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// adminUserUpdate: losing admin or being deactivated ends the user's sessions;
// becoming admin rotates the session identifier.
func (s *Server) adminUserUpdate(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), pathID(r, "id")
	var in struct {
		IsAdmin  bool `json:"is_admin"`
		IsActive bool `json:"is_active"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	old, err := s.Store.GetUserByID(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if id == principal(r).UserID && (!in.IsAdmin || !in.IsActive) {
		s.fail(w, r, model.Invalid("Anda tidak dapat mencabut hak admin atau menonaktifkan akun Anda sendiri."))
		return
	}
	err = s.Store.UpdateUserFlags(ctx, id, in.IsAdmin, in.IsActive)
	if err == nil && ((old.IsAdmin && !in.IsAdmin) || (old.IsActive && !in.IsActive)) {
		err = s.Sessions.RevokeUser(ctx, id)
	} else if err == nil && !old.IsAdmin && in.IsAdmin {
		err = s.Sessions.RequireRotation(ctx, id)
	}
	if err == nil {
		audit(r, "admin.user.update", "user_id", id, "is_admin", in.IsAdmin, "is_active", in.IsActive)
	}
	s.done(w, r, err, "User diperbarui.")
}

// adminUserPassword sets a new password and ends every session of the user.
func (s *Server) adminUserPassword(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), pathID(r, "id")
	var in struct{ Password string }
	err := decode(r, &in)
	var hash string
	if err == nil {
		hash, err = auth.HashPassword(in.Password)
	}
	if err == nil {
		err = s.Store.SetPassword(ctx, id, hash)
	}
	if err == nil {
		err = s.Sessions.RevokeUser(ctx, id)
		audit(r, "admin.user.password", "user_id", id)
	}
	s.done(w, r, invalid(err), "Password diganti dan semua session user diakhiri.")
}

func (s *Server) adminUserDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	if id == principal(r).UserID {
		s.fail(w, r, model.Invalid("Anda tidak dapat menghapus akun Anda sendiri."))
		return
	}
	err := s.Store.DeleteUser(r.Context(), id)
	if err == nil {
		audit(r, "admin.user.delete", "user_id", id)
	}
	s.done(w, r, err, "User dihapus.")
}

// invalid shows password and username rule errors as validation errors.
func invalid(err error) error {
	if err == auth.ErrPasswordTooShort || err == auth.ErrPasswordTooLong || err == auth.ErrInvalidUsername {
		return model.Invalid(err.Error())
	}
	return err
}

// ---- groups -----------------------------------------------------------------------

func (s *Server) adminGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.Store.ListGroups(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

func (s *Server) adminGroupCreate(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name string }
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 64 {
		s.fail(w, r, model.Invalid("Nama group wajib diisi (maksimal 64 karakter)."))
		return
	}
	id, err := s.Store.CreateGroup(r.Context(), name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	audit(r, "admin.group.create", "group_id", id)
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) adminGroup(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), pathID(r, "id")
	g, err := s.Store.GetGroup(ctx, id)
	var members, users []model.User
	var grants []model.Grant
	var dbs []model.Database
	if err == nil {
		members, err = s.Store.ListMembers(ctx, id)
	}
	if err == nil {
		grants, err = s.Store.ListGrants(ctx, id)
	}
	if err == nil {
		users, err = s.Store.ListUsers(ctx)
	}
	if err == nil {
		dbs, err = s.Store.ListDatabases(ctx)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"group": g, "members": nonNil(members), "grants": nonNil(grants),
		"users": nonNil(users), "databases": nonNil(dbs)})
}

func nonNil[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}

// eachMember calls fn for every member of a group.
func (s *Server) eachMember(ctx context.Context, groupID int64, fn func(context.Context, int64) error) error {
	members, err := s.Store.ListMembers(ctx, groupID)
	for _, m := range members {
		if err == nil {
			err = fn(ctx, m.ID)
		}
	}
	return err
}

func (s *Server) adminGroupDelete(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), pathID(r, "id")
	err := s.eachMember(ctx, id, s.Sessions.RevokeUser)
	if err == nil {
		err = s.Store.DeleteGroup(ctx, id)
	}
	if err == nil {
		audit(r, "admin.group.delete", "group_id", id)
	}
	s.done(w, r, err, "Group dihapus.")
}

// adminGroupSetLeader: the leader approves change requests for the group.
// Gaining that power rotates the session identifier.
func (s *Server) adminGroupSetLeader(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), pathID(r, "id")
	var in struct {
		UserID *int64 `json:"user_id"`
	}
	err := decode(r, &in)
	if err == nil {
		err = s.Store.SetLeader(ctx, id, in.UserID)
	}
	if err == nil && in.UserID != nil {
		err = s.Sessions.RequireRotation(ctx, *in.UserID)
	}
	if err == nil {
		audit(r, "admin.group.leader", "group_id", id, "leader_id", in.UserID)
	}
	s.done(w, r, err, "Leader group disimpan.")
}

func (s *Server) adminGroupAddMember(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), pathID(r, "id")
	var in struct {
		UserID int64 `json:"user_id"`
	}
	err := decode(r, &in)
	if err == nil {
		err = s.Store.AddMember(ctx, id, in.UserID)
	}
	if err == nil {
		err = s.Sessions.RequireRotation(ctx, in.UserID)
	}
	if err == nil {
		audit(r, "admin.group.member.add", "group_id", id, "user_id", in.UserID)
	}
	s.done(w, r, err, "Anggota ditambahkan.")
}

func (s *Server) adminGroupRemoveMember(w http.ResponseWriter, r *http.Request) {
	ctx, id, uid := r.Context(), pathID(r, "id"), pathID(r, "userID")
	err := s.Store.RemoveMember(ctx, id, uid)
	if err == nil {
		err = s.Sessions.RevokeUser(ctx, uid)
	}
	if err == nil {
		audit(r, "admin.group.member.remove", "group_id", id, "user_id", uid)
	}
	s.done(w, r, err, "Anggota dikeluarkan dari group dan session-nya diakhiri.")
}

// adminGroupSetGrant: a higher level rotates the members' sessions; a lower
// level ends them.
func (s *Server) adminGroupSetGrant(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), pathID(r, "id")
	var in struct {
		DatabaseID int64  `json:"database_id"`
		Level      string `json:"level"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	level, err := authz.ParseLevel(in.Level)
	if err != nil {
		s.fail(w, r, model.Invalid("Tingkat akses tidak valid."))
		return
	}
	old, err := s.Store.GetGrant(ctx, id, in.DatabaseID)
	if err == nil {
		err = s.Store.SetGrant(ctx, id, in.DatabaseID, level)
	}
	if err == nil && level > old {
		err = s.eachMember(ctx, id, s.Sessions.RequireRotation)
	} else if err == nil && level < old {
		err = s.eachMember(ctx, id, s.Sessions.RevokeUser)
	}
	if err == nil {
		audit(r, "admin.group.grant", "group_id", id, "database", in.DatabaseID, "level", level.String())
	}
	s.done(w, r, err, "Akses database disimpan.")
}

func (s *Server) adminGroupRemoveGrant(w http.ResponseWriter, r *http.Request) {
	ctx, id, dbID := r.Context(), pathID(r, "id"), pathID(r, "dbID")
	err := s.Store.RemoveGrant(ctx, id, dbID)
	if err == nil {
		err = s.eachMember(ctx, id, s.Sessions.RevokeUser)
	}
	if err == nil {
		audit(r, "admin.group.grant.remove", "group_id", id, "database", dbID)
	}
	s.done(w, r, err, "Akses database dicabut dan session anggota group diakhiri.")
}

// ---- database connections ----------------------------------------------------

func (s *Server) adminDatabases(w http.ResponseWriter, r *http.Request) {
	dbs, err := s.Store.ListDatabases(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(dbs))
}

var sslModes = []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}

func (s *Server) adminDatabase(w http.ResponseWriter, r *http.Request) {
	db, err := s.Store.GetDatabase(r.Context(), pathID(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, db) // the password is never returned
}

// dbForm validates a connection; a new password is tested before it is saved.
func (s *Server) dbForm(r *http.Request, requirePassword bool) (model.Database, error) {
	var in struct {
		Name     string `json:"name"`
		Host     string `json:"host"`
		Port     int    `json:"port"`
		DBName   string `json:"dbname"`
		Username string `json:"username"`
		Password string `json:"password"`
		SSLMode  string `json:"sslmode"`
	}
	if err := decode(r, &in); err != nil {
		return model.Database{}, err
	}
	p := targetdb.Params{Host: strings.TrimSpace(in.Host), Port: in.Port, DBName: strings.TrimSpace(in.DBName),
		Username: strings.TrimSpace(in.Username), Password: in.Password, SSLMode: in.SSLMode}
	d := model.Database{ID: pathID(r, "id"), Name: strings.TrimSpace(in.Name),
		Host: p.Host, Port: p.Port, DBName: p.DBName, Username: p.Username, SSLMode: p.SSLMode}
	switch {
	case d.Name == "" || d.Host == "" || d.DBName == "" || d.Username == "":
		return d, model.Invalid("Nama koneksi, host, nama database, dan username wajib diisi.")
	case d.Port < 1 || d.Port > 65535:
		return d, model.Invalid("Port harus antara 1 dan 65535.")
	case !slices.Contains(sslModes, d.SSLMode):
		return d, model.Invalid("SSL mode tidak valid.")
	case requirePassword && p.Password == "":
		return d, model.Invalid("Password database wajib diisi.")
	case p.Password == "":
		return d, nil
	}
	if err := targetdb.Test(r.Context(), p); err != nil {
		return d, model.Invalid(err.Error())
	}
	var err error
	d.PasswordEnc, err = s.Box.Encrypt([]byte(p.Password))
	return d, err
}

func (s *Server) adminDatabaseCreate(w http.ResponseWriter, r *http.Request) {
	d, err := s.dbForm(r, true)
	if err == nil {
		d.ID, err = s.Store.CreateDatabase(r.Context(), d)
	}
	if errors.Is(err, model.ErrConflict) {
		err = model.Invalid("Nama koneksi sudah dipakai.")
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	audit(r, "admin.database.create", "database", d.ID)
	writeJSON(w, http.StatusCreated, map[string]int64{"id": d.ID})
}

func (s *Server) adminDatabaseUpdate(w http.ResponseWriter, r *http.Request) {
	d, err := s.dbForm(r, false)
	if err == nil {
		err = s.Store.UpdateDatabase(r.Context(), d)
	}
	if errors.Is(err, model.ErrConflict) {
		err = model.Invalid("Nama koneksi sudah dipakai.")
	}
	s.Targets.Invalidate(d.ID)
	if err == nil {
		audit(r, "admin.database.update", "database", d.ID)
	}
	s.done(w, r, err, "Koneksi database diperbarui.")
}

func (s *Server) adminDatabaseDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	err := s.Store.DeleteDatabase(r.Context(), id)
	s.Targets.Invalidate(id)
	if err == nil {
		audit(r, "admin.database.delete", "database", id)
	}
	s.done(w, r, err, "Koneksi database dihapus.")
}
