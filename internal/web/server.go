package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"dbmp/internal/auth"
	"dbmp/internal/authz"
	"dbmp/internal/model"
	"dbmp/internal/secret"
	"dbmp/internal/session"
	"dbmp/internal/store"
	"dbmp/internal/targetdb"
)

const (
	sessionCookie   = "dbmp_session"
	loginCSRFCookie = "dbmp_login_csrf"
	flashCookie     = "dbmp_flash"
	csrfField       = "csrf_token"
)

type Server struct {
	Store        *store.Store
	Sessions     *session.Manager
	Auth         *auth.Service
	Policy       *authz.Policy
	Targets      *targetdb.Manager
	Box          *secret.Box
	Assets       fs.FS
	CookieSecure bool // set the Secure flag on cookies (behind HTTPS)

	tpl     *Renderer
	handler http.Handler
}

// Init parses the templates and builds the routes.
func (s *Server) Init() error {
	tpl, err := NewRenderer(s.Assets)
	if err != nil {
		return err
	}
	s.tpl = tpl
	s.handler = s.routes()
	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(s.Assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))

	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.authed(s.logout))
	mux.HandleFunc("GET /{$}", s.authed(s.dashboard))

	mux.HandleFunc("GET /db/{dbID}", s.authed(s.dbTables))
	mux.HandleFunc("GET /db/{dbID}/tables/{schema}/{table}", s.authed(s.dbTable))

	mux.HandleFunc("GET /db/{dbID}/migrations", s.authed(s.migrationList))
	mux.HandleFunc("GET /db/{dbID}/migrations/new", s.authed(s.migrationNew))
	mux.HandleFunc("POST /db/{dbID}/migrations", s.authed(s.migrationCreate))
	mux.HandleFunc("GET /db/{dbID}/migrations/{mID}", s.authed(s.migrationDetail))
	mux.HandleFunc("POST /db/{dbID}/migrations/{mID}", s.authed(s.migrationUpdate))
	mux.HandleFunc("POST /db/{dbID}/migrations/{mID}/execute", s.authed(s.migrationExecute))
	mux.HandleFunc("POST /db/{dbID}/migrations/{mID}/explain", s.authed(s.migrationExplain))
	mux.HandleFunc("POST /db/{dbID}/migrations/{mID}/delete", s.authed(s.migrationDelete))

	mux.HandleFunc("GET /admin/users", s.admin(s.adminUsers))
	mux.HandleFunc("POST /admin/users", s.admin(s.adminUserCreate))
	mux.HandleFunc("GET /admin/users/{id}", s.admin(s.adminUser))
	mux.HandleFunc("POST /admin/users/{id}", s.admin(s.adminUserUpdate))
	mux.HandleFunc("POST /admin/users/{id}/password", s.admin(s.adminUserPassword))
	mux.HandleFunc("POST /admin/users/{id}/delete", s.admin(s.adminUserDelete))

	mux.HandleFunc("GET /admin/groups", s.admin(s.adminGroups))
	mux.HandleFunc("POST /admin/groups", s.admin(s.adminGroupCreate))
	mux.HandleFunc("GET /admin/groups/{id}", s.admin(s.adminGroup))
	mux.HandleFunc("POST /admin/groups/{id}/delete", s.admin(s.adminGroupDelete))
	mux.HandleFunc("POST /admin/groups/{id}/members", s.admin(s.adminGroupAddMember))
	mux.HandleFunc("POST /admin/groups/{id}/members/{userID}/remove", s.admin(s.adminGroupRemoveMember))
	mux.HandleFunc("POST /admin/groups/{id}/grants", s.admin(s.adminGroupSetGrant))
	mux.HandleFunc("POST /admin/groups/{id}/grants/{dbID}/remove", s.admin(s.adminGroupRemoveGrant))

	mux.HandleFunc("GET /admin/databases", s.admin(s.adminDatabases))
	mux.HandleFunc("GET /admin/databases/new", s.admin(s.adminDatabaseNew))
	mux.HandleFunc("POST /admin/databases", s.admin(s.adminDatabaseCreate))
	mux.HandleFunc("GET /admin/databases/{id}", s.admin(s.adminDatabase))
	mux.HandleFunc("POST /admin/databases/{id}", s.admin(s.adminDatabaseUpdate))
	mux.HandleFunc("POST /admin/databases/{id}/delete", s.admin(s.adminDatabaseDelete))

	return securityHeaders(s.loadSession(mux))
}


type ctxKey int

const (
	ctxPrincipal ctxKey = iota
	ctxSession
	ctxToken
)

func principal(r *http.Request) *authz.Principal {
	p, _ := r.Context().Value(ctxPrincipal).(*authz.Principal)
	return p
}

func currentSession(r *http.Request) *session.Session {
	s, _ := r.Context().Value(ctxSession).(*session.Session)
	return s
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		c, err := r.Cookie(sessionCookie)
		if err != nil || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		p, sess, err := s.Auth.Authenticate(r.Context(), c.Value)
		if err != nil {
			if !errors.Is(err, session.ErrInvalid) {
				slog.Error("session validation failed", "error", err)
			}
			s.clearCookie(w, sessionCookie)
			next.ServeHTTP(w, r)
			return
		}
		token := c.Value
		if sess.NewToken != "" {
			token = sess.NewToken
			s.setSessionCookie(w, token, sess.AbsoluteExpiresAt)
		}
		ctx := context.WithValue(r.Context(), ctxPrincipal, p)
		ctx = context.WithValue(ctx, ctxSession, sess)
		ctx = context.WithValue(ctx, ctxToken, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if principal(r) == nil {
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			} else {
				http.Error(w, "Sesi tidak valid atau telah berakhir. Silakan login kembali.", http.StatusUnauthorized)
			}
			return
		}
		if r.Method == http.MethodPost && (!sameOrigin(r) || !validCSRF(r, currentSession(r).CSRFToken)) {
			s.renderError(w, r, http.StatusForbidden, "Permintaan ditolak: token CSRF tidak valid. Muat ulang halaman lalu coba lagi.")
			return
		}
		h(w, r)
	}
}

func (s *Server) admin(h http.HandlerFunc) http.HandlerFunc {
	return s.authed(func(w http.ResponseWriter, r *http.Request) {
		if err := s.Policy.Authorize(r.Context(), principal(r), authz.ActionManagePlatform, 0); err != nil {
			s.fail(w, r, err)
			return
		}
		h(w, r)
	})
}

func validCSRF(r *http.Request, expected string) bool {
	got := r.PostFormValue(csrfField)
	return got != "" && expected != "" && subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && origin != "null" && strings.EqualFold(u.Host, r.Host)
}


func (s *Server) cookie(name, value string, maxAge int, sameSite http.SameSite) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: s.CookieSecure, SameSite: sameSite}
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, s.cookie(sessionCookie, token, int(time.Until(expires).Seconds()), http.SameSiteLaxMode))
}

func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, s.cookie(name, "", -1, http.SameSiteLaxMode))
}

func (s *Server) flash(w http.ResponseWriter, kind, msg string) {
	http.SetCookie(w, s.cookie(flashCookie, url.QueryEscape(kind+"|"+msg), 60, http.SameSiteLaxMode))
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) page(w http.ResponseWriter, r *http.Request, title string, data any) Page {
	p := Page{Title: title, User: principal(r), Data: data}
	if sess := currentSession(r); sess != nil {
		p.CSRF = sess.CSRFToken
	}
	if c, err := r.Cookie(flashCookie); err == nil {
		s.clearCookie(w, flashCookie)
		v, _ := url.QueryUnescape(c.Value)
		p.FlashKind, p.Flash, _ = strings.Cut(v, "|")
	}
	return p
}

func (s *Server) render(w http.ResponseWriter, status int, name string, p Page) {
	if err := s.tpl.Render(w, status, name, p); err != nil {
		slog.Error("render failed", "page", name, "error", err)
	}
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	p := s.page(w, r, http.StatusText(status), nil)
	p.Error = msg
	s.render(w, status, "error", p)
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ve *model.ValidationError
	switch {
	case errors.Is(err, authz.ErrForbidden):
		s.renderError(w, r, http.StatusForbidden, "Akses ditolak: Anda tidak memiliki hak akses untuk tindakan ini.")
	case errors.Is(err, model.ErrNotFound):
		s.renderError(w, r, http.StatusNotFound, "Data yang diminta tidak ditemukan.")
	case errors.As(err, &ve):
		s.renderError(w, r, http.StatusBadRequest, ve.Msg)
	case targetdb.IsConnError(err):
		s.renderError(w, r, http.StatusBadGateway, "Tidak dapat terhubung ke database target. Periksa konfigurasi koneksi atau hubungi Platform Admin. Detail: "+err.Error())
	case pgCode(err) == "42501":
		s.renderError(w, r, http.StatusForbidden, "Database target menolak perintah karena role koneksi tidak memiliki hak: "+targetdb.PgErrorMessage(err))
	case pgCode(err) != "":
		s.renderError(w, r, http.StatusBadGateway, "Database target mengembalikan error: "+targetdb.PgErrorMessage(err))
	case errors.Is(err, context.DeadlineExceeded):
		s.renderError(w, r, http.StatusGatewayTimeout, "Permintaan ke database melebihi batas waktu.")
	default:
		slog.Error("request failed", "path", r.URL.Path, "error", err)
		s.renderError(w, r, http.StatusInternalServerError, "Terjadi kesalahan pada server.")
	}
}

func (s *Server) done(w http.ResponseWriter, r *http.Request, err error, okMsg, target string) {
	var ve *model.ValidationError
	switch {
	case err == nil:
		s.flash(w, "ok", okMsg)
	case errors.As(err, &ve):
		s.flash(w, "bad", ve.Msg)
	case errors.Is(err, model.ErrConflict):
		s.flash(w, "bad", "Nama sudah dipakai.")
	default:
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func pathID(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id
}

func formID(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(r.PostFormValue(name), 10, 64)
	return id
}

// safeNext only allows local redirect targets after login.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}
