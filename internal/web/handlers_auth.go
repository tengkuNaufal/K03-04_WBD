package web

import (
	"errors"
	"log/slog"
	"net/http"

	"dbmp/internal/auth"
	"dbmp/internal/authz"
	"dbmp/internal/model"
)

type sessionView struct {
	User *authz.Principal `json:"user"`
	CSRF string           `json:"csrf"`
}

func (s *Server) sessionInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, sessionView{User: principal(r), CSRF: currentSession(r).CSRFToken})
}

// login: JSON only and same-origin, so a cross-site page cannot submit it.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password string }
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "Permintaan lintas situs ditolak.")
		return
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	var previous string
	if old, err := r.Cookie(sessionCookie); err == nil {
		previous = old.Value
	}
	token, sess, err := s.Auth.Login(r.Context(), in.Username, in.Password, previous)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		slog.InfoContext(r.Context(), "login failed", "event", "auth.login", "status", "failed", "actor", auth.NormalizeUsername(in.Username))
		writeError(w, http.StatusUnauthorized, "Username atau password salah.")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.setSessionCookie(w, token, sess.AbsoluteExpiresAt)
	p, _, err := s.Auth.Authenticate(r.Context(), token)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	slog.InfoContext(r.Context(), "login succeeded", "event", "auth.login", "status", "ok", "actor", p.Username)
	writeJSON(w, http.StatusOK, sessionView{User: p, CSRF: sess.CSRFToken})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	token, _ := r.Context().Value(ctxToken).(string)
	if err := s.Auth.Logout(r.Context(), token); err != nil {
		s.fail(w, r, err)
		return
	}
	s.clearCookie(w)
	slog.InfoContext(r.Context(), "logout", "event", "auth.logout", "status", "ok", "actor", principal(r).Username)
	writeOK(w, "Logout berhasil.")
}

type databaseView struct {
	ID    int64       `json:"id"`
	Name  string      `json:"name"`
	Level authz.Level `json:"level"`
}

func (s *Server) databases(w http.ResponseWriter, r *http.Request) {
	dbs, err := s.Store.AccessibleDatabases(r.Context(), principal(r).UserID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := []databaseView{}
	for _, d := range dbs {
		out = append(out, databaseView{ID: d.Database.ID, Name: d.Database.Name, Level: d.Level})
	}
	writeJSON(w, http.StatusOK, out)
}

// reviews lists requests waiting for the user's approval as a group leader.
func (s *Server) reviews(w http.ResponseWriter, r *http.Request) {
	rs, err := s.Store.AwaitingApproval(r.Context(), principal(r).UserID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if rs == nil {
		rs = []model.Request{}
	}
	writeJSON(w, http.StatusOK, rs)
}
