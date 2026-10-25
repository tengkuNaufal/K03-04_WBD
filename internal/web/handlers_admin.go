package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"dbmp/internal/auth"
	"dbmp/internal/authz"
	"dbmp/internal/model"
	"dbmp/internal/targetdb"
)

func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.ListUsers(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, http.StatusOK, "admin_users", s.page(w, r, "Users", users))
}

func (s *Server) adminUserCreate(w http.ResponseWriter, r *http.Request) {
	username := auth.NormalizeUsername(r.PostFormValue("username"))
	err := auth.ValidateUsername(username)
	var hash string
	if err == nil {
		hash, err = auth.HashPassword(r.PostFormValue("password"))
	}
	if err == nil {
		_, err = s.Store.CreateUser(r.Context(), username, hash, r.PostFormValue("is_admin") == "1")
	}
	s.done(w, r, invalid(err), "User dibuat.", "/admin/users")
}

func (s *Server) adminUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.Store.GetUserByID(r.Context(), pathID(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, http.StatusOK, "admin_user", s.page(w, r, u.Username, u))
}

func (s *Server) adminUserUpdate(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), pathID(r, "id")
	isAdmin, isActive := r.PostFormValue("is_admin") == "1", r.PostFormValue("is_active") == "1"
	back := fmt.Sprintf("/admin/users/%d", id)
	old, err := s.Store.GetUserByID(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if id == principal(r).UserID && (!isAdmin || !isActive) {
		s.done(w, r, model.Invalid("Anda tidak dapat mencabut hak admin atau menonaktifkan akun Anda sendiri."), "", back)
		return
	}
	err = s.Store.UpdateUserFlags(ctx, id, isAdmin, isActive)
	if err == nil && ((old.IsAdmin && !isAdmin) || (old.IsActive && !isActive)) {
		err = s.Sessions.RevokeUser(ctx, id)
	} else if err == nil && !old.IsAdmin && isAdmin {
		err = s.Sessions.RequireRotation(ctx, id)
	}
	s.done(w, r, err, "User diperbarui.", back)
}

// adminUserPassword sets a new password and ends every session of the user.
func (s *Server) adminUserPassword(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), pathID(r, "id")
	hash, err := auth.HashPassword(r.PostFormValue("password"))
	if err == nil {
		err = s.Store.SetPassword(ctx, id, hash)
	}
	if err == nil {
		err = s.Sessions.RevokeUser(ctx, id)
	}
	s.done(w, r, invalid(err), "Password diganti dan semua session user diakhiri.", fmt.Sprintf("/admin/users/%d", id))
}

func (s *Server) adminUserDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	if id == principal(r).UserID {
		s.done(w, r, model.Invalid("Anda tidak dapat menghapus akun Anda sendiri."), "", fmt.Sprintf("/admin/users/%d", id))
		return
	}
	s.done(w, r, s.Store.DeleteUser(r.Context(), id), "User dihapus.", "/admin/users")
}

// invalid shows password and username rule errors as validation errors.
func invalid(err error) error {
	if err == auth.ErrPasswordTooShort || err == auth.ErrPasswordTooLong || err == auth.ErrInvalidUsername {
		return model.Invalid(err.Error())
	}
	return err
}

func (s *Server) adminGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.Store.ListGroups(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, http.StatusOK, "admin_groups", s.page(w, r, "Groups", groups))
}

func (s *Server) adminGroupCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" || len(name) > 64 {
		s.done(w, r, model.Invalid("Nama group wajib diisi (maksimal 64 karakter)."), "", "/admin/groups")
		return
	}
	id, err := s.Store.CreateGroup(r.Context(), name)
	if err != nil {
		s.done(w, r, err, "", "/admin/groups")
		return
	}
	s.done(w, r, nil, "Group dibuat.", fmt.Sprintf("/admin/groups/%d", id))
}

type groupData struct {
	Group     model.Group
	Members   []model.User
	Grants    []model.Grant
	Users     []model.User
	Databases []model.Database
}

func (s *Server) adminGroup(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), pathID(r, "id")
	var d groupData
	var err error
	if d.Group, err = s.Store.GetGroup(ctx, id); err == nil {
		d.Members, err = s.Store.ListMembers(ctx, id)
	}
	if err == nil {
		d.Grants, err = s.Store.ListGrants(ctx, id)
	}
	if err == nil {
		d.Users, err = s.Store.ListUsers(ctx)
	}
	if err == nil {
		d.Databases, err = s.Store.ListDatabases(ctx)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, http.StatusOK, "admin_group", s.page(w, r, d.Group.Name, d))
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
	s.done(w, r, err, "Group dihapus.", "/admin/groups")
}

func (s *Server) adminGroupAddMember(w http.ResponseWriter, r *http.Request) {
	ctx, id, uid := r.Context(), pathID(r, "id"), formID(r, "user_id")
	err := s.Store.AddMember(ctx, id, uid)
	if err == nil {
		err = s.Sessions.RequireRotation(ctx, uid)
	}
	s.done(w, r, err, "Anggota ditambahkan.", fmt.Sprintf("/admin/groups/%d", id))
}

func (s *Server) adminGroupRemoveMember(w http.ResponseWriter, r *http.Request) {
	ctx, id, uid := r.Context(), pathID(r, "id"), pathID(r, "userID")
	err := s.Store.RemoveMember(ctx, id, uid)
	if err == nil {
		err = s.Sessions.RevokeUser(ctx, uid)
	}
	s.done(w, r, err, "Anggota dikeluarkan dari group dan session-nya diakhiri.", fmt.Sprintf("/admin/groups/%d", id))
}

func (s *Server) adminGroupSetGrant(w http.ResponseWriter, r *http.Request) {
	ctx, id, dbID := r.Context(), pathID(r, "id"), formID(r, "database_id")
	back := fmt.Sprintf("/admin/groups/%d", id)
	level, err := authz.ParseLevel(r.PostFormValue("level"))
	if err != nil {
		s.done(w, r, model.Invalid("Tingkat akses tidak valid."), "", back)
		return
	}
	old, err := s.Store.GetGrant(ctx, id, dbID)
	if err == nil {
		err = s.Store.SetGrant(ctx, id, dbID, level)
	}
	if err == nil && level > old {
		err = s.eachMember(ctx, id, s.Sessions.RequireRotation)
	} else if err == nil && level < old {
		err = s.eachMember(ctx, id, s.Sessions.RevokeUser)
	}
	s.done(w, r, err, "Akses database disimpan.", back)
}

func (s *Server) adminGroupRemoveGrant(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), pathID(r, "id")
	err := s.Store.RemoveGrant(ctx, id, pathID(r, "dbID"))
	if err == nil {
		err = s.eachMember(ctx, id, s.Sessions.RevokeUser)
	}
	s.done(w, r, err, "Akses database dicabut dan session anggota group diakhiri.", fmt.Sprintf("/admin/groups/%d", id))
}

// ---- database connections --------------------------------------------------

func (s *Server) adminDatabases(w http.ResponseWriter, r *http.Request) {
	dbs, err := s.Store.ListDatabases(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, http.StatusOK, "admin_databases", s.page(w, r, "Koneksi Database", dbs))
}

var sslModes = []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}

type dbFormData struct {
	DB       model.Database
	SSLModes []string
}

func (s *Server) adminDatabaseNew(w http.ResponseWriter, r *http.Request) {
	d := dbFormData{DB: model.Database{Port: 5432, SSLMode: "disable"}, SSLModes: sslModes}
	s.render(w, http.StatusOK, "admin_database", s.page(w, r, "Koneksi baru", d))
}

func (s *Server) adminDatabase(w http.ResponseWriter, r *http.Request) {
	db, err := s.Store.GetDatabase(r.Context(), pathID(r, "id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, http.StatusOK, "admin_database", s.page(w, r, db.Name, dbFormData{db, sslModes}))
}

func (s *Server) dbForm(r *http.Request, requirePassword bool) (model.Database, error) {
	port, _ := strconv.Atoi(r.PostFormValue("port"))
	p := targetdb.Params{
		Host: strings.TrimSpace(r.PostFormValue("host")), Port: port, DBName: strings.TrimSpace(r.PostFormValue("dbname")),
		Username: strings.TrimSpace(r.PostFormValue("username")), Password: r.PostFormValue("password"), SSLMode: r.PostFormValue("sslmode"),
	}
	d := model.Database{ID: pathID(r, "id"), Name: strings.TrimSpace(r.PostFormValue("name")),
		Host: p.Host, Port: p.Port, DBName: p.DBName, Username: p.Username, SSLMode: p.SSLMode}
	switch {
	case d.Name == "" || d.Host == "" || d.DBName == "" || d.Username == "":
		return d, model.Invalid("Nama koneksi, host, nama database, dan username wajib diisi.")
	case port < 1 || port > 65535:
		return d, model.Invalid("Port harus antara 1 dan 65535.")
	case !slices.Contains(sslModes, d.SSLMode):
		return d, model.Invalid("SSL mode tidak valid.")
	case requirePassword && p.Password == "":
		return d, model.Invalid("Password database wajib diisi.")
	case p.Password == "":
		return d, nil
	}
	// A new password is tested before it is saved.
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
	var ve *model.ValidationError
	if errors.As(err, &ve) {
		// Show the form again; the password is never echoed back.
		p := s.page(w, r, "Koneksi baru", dbFormData{d, sslModes})
		p.Error = ve.Msg
		s.render(w, http.StatusBadRequest, "admin_database", p)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.done(w, r, nil, "Koneksi database ditambahkan dan berhasil diuji.", fmt.Sprintf("/admin/databases/%d", d.ID))
}

func (s *Server) adminDatabaseUpdate(w http.ResponseWriter, r *http.Request) {
	d, err := s.dbForm(r, false)
	if err == nil {
		err = s.Store.UpdateDatabase(r.Context(), d)
	}
	s.Targets.Invalidate(d.ID)
	s.done(w, r, err, "Koneksi database diperbarui.", fmt.Sprintf("/admin/databases/%d", d.ID))
}

func (s *Server) adminDatabaseDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	err := s.Store.DeleteDatabase(r.Context(), id)
	s.Targets.Invalidate(id)
	s.done(w, r, err, "Koneksi database dihapus.", "/admin/databases")
}
