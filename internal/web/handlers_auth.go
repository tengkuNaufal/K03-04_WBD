package web

import (
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"

	"dbmp/internal/auth"
)

type loginData struct{ Username, Next string }

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	if principal(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.renderLogin(w, r, http.StatusOK, loginData{Next: r.URL.Query().Get("next")}, "")
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, status int, d loginData, errMsg string) {
	token := randomToken()
	http.SetCookie(w, s.cookie(loginCSRFCookie, token, 3600, http.SameSiteStrictMode))
	p := s.page(w, r, "Login", d)
	p.CSRF, p.Error = token, errMsg
	s.render(w, status, "login", p)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	d := loginData{Username: r.PostFormValue("username"), Next: r.PostFormValue("next")}
	c, err := r.Cookie(loginCSRFCookie)
	form := r.PostFormValue(csrfField)
	if err != nil || form == "" || !sameOrigin(r) || subtle.ConstantTimeCompare([]byte(c.Value), []byte(form)) != 1 {
		s.renderLogin(w, r, http.StatusForbidden, d, "Sesi form login kedaluwarsa. Silakan coba lagi.")
		return
	}

	var previous string
	if old, err := r.Cookie(sessionCookie); err == nil {
		previous = old.Value
	}
	token, sess, err := s.Auth.Login(r.Context(), d.Username, r.PostFormValue("password"), previous)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		slog.Info("login failed", "username", auth.NormalizeUsername(d.Username))
		s.renderLogin(w, r, http.StatusUnauthorized, d, "Username atau password salah.")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.clearCookie(w, loginCSRFCookie)
	s.setSessionCookie(w, token, sess.AbsoluteExpiresAt)
	slog.Info("login succeeded", "user_id", sess.UserID)
	http.Redirect(w, r, safeNext(d.Next), http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	token, _ := r.Context().Value(ctxToken).(string)
	if err := s.Auth.Logout(r.Context(), token); err != nil {
		s.fail(w, r, err)
		return
	}
	s.clearCookie(w, sessionCookie)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	dbs, err := s.Store.AccessibleDatabases(r.Context(), principal(r).UserID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, http.StatusOK, "dashboard", s.page(w, r, "Dashboard", dbs))
}
