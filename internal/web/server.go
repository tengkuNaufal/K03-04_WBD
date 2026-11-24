// Package web is the JSON API used by the SPA, plus the SPA files.
package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"dbmp/internal/auth"
	"dbmp/internal/authz"
	"dbmp/internal/model"
	"dbmp/internal/obs"
	"dbmp/internal/secret"
	"dbmp/internal/session"
	"dbmp/internal/store"
	"dbmp/internal/targetdb"
	"dbmp/internal/workflow"
)

const (
	sessionCookie = "dbmp_session"
	csrfHeader    = "X-CSRF-Token"
)

type Server struct {
	Store        *store.Store
	Sessions     *session.Manager
	Auth         *auth.Service
	Policy       *authz.Policy
	Targets      *targetdb.Manager
	Box          *secret.Box
	Assets       fs.FS // built SPA (index.html, assets/)
	CookieSecure bool  // set the Secure flag on cookies (behind HTTPS)

	handler http.Handler
}

func (s *Server) Init() error {
	s.handler = s.routes()
	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	h := func(pattern string, fn http.HandlerFunc) {
		mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if info := infoOf(r); info != nil {
				info.route = pattern
			}
			trace.SpanFromContext(r.Context()).SetName(pattern)
			fn(w, r)
		}))
	}

	h("GET /api/session", s.authed(s.sessionInfo))
	h("POST /api/login", s.login)
	h("POST /api/logout", s.authed(s.logout))
	h("GET /api/databases", s.authed(s.databases))
	h("GET /api/reviews", s.authed(s.reviews))

	h("GET /api/db/{dbID}", s.authed(s.dbInfo))
	h("GET /api/db/{dbID}/tables", s.authed(s.dbTables))
	h("GET /api/db/{dbID}/tables/{schema}/{table}", s.authed(s.dbTable))
	h("GET /api/db/{dbID}/tables/{schema}/{table}/rows", s.authed(s.dbRows))

	h("GET /api/db/{dbID}/migrations", s.authed(s.migrationList))
	h("POST /api/db/{dbID}/migrations", s.authed(s.migrationCreate))
	h("GET /api/db/{dbID}/migrations/{mID}", s.authed(s.migrationDetail))
	h("PUT /api/db/{dbID}/migrations/{mID}", s.authed(s.migrationUpdate))
	h("DELETE /api/db/{dbID}/migrations/{mID}", s.authed(s.migrationDelete))
	h("POST /api/db/{dbID}/migrations/{mID}/validate", s.authed(s.migrationValidate))
	h("POST /api/db/{dbID}/migrations/{mID}/requests", s.authed(s.requestSubmit))

	h("GET /api/db/{dbID}/requests", s.authed(s.requestList))
	h("GET /api/db/{dbID}/requests/{rID}", s.authed(s.requestDetail))
	h("POST /api/db/{dbID}/requests/{rID}/approve", s.authed(s.requestApprove))
	h("POST /api/db/{dbID}/requests/{rID}/reject", s.authed(s.requestReject))
	h("POST /api/db/{dbID}/requests/{rID}/cancel", s.authed(s.requestCancel))
	h("POST /api/db/{dbID}/requests/{rID}/execute", s.authed(s.requestExecute))
	h("GET /api/db/{dbID}/jobs/{jobID}", s.authed(s.jobDetail))
	h("GET /api/db/{dbID}/history", s.authed(s.history))

	h("GET /api/admin/users", s.admin(s.adminUsers))
	h("POST /api/admin/users", s.admin(s.adminUserCreate))
	h("GET /api/admin/users/{id}", s.admin(s.adminUser))
	h("PUT /api/admin/users/{id}", s.admin(s.adminUserUpdate))
	h("POST /api/admin/users/{id}/password", s.admin(s.adminUserPassword))
	h("DELETE /api/admin/users/{id}", s.admin(s.adminUserDelete))

	h("GET /api/admin/groups", s.admin(s.adminGroups))
	h("POST /api/admin/groups", s.admin(s.adminGroupCreate))
	h("GET /api/admin/groups/{id}", s.admin(s.adminGroup))
	h("DELETE /api/admin/groups/{id}", s.admin(s.adminGroupDelete))
	h("PUT /api/admin/groups/{id}/leader", s.admin(s.adminGroupSetLeader))
	h("POST /api/admin/groups/{id}/members", s.admin(s.adminGroupAddMember))
	h("DELETE /api/admin/groups/{id}/members/{userID}", s.admin(s.adminGroupRemoveMember))
	h("PUT /api/admin/groups/{id}/grants", s.admin(s.adminGroupSetGrant))
	h("DELETE /api/admin/groups/{id}/grants/{dbID}", s.admin(s.adminGroupRemoveGrant))

	h("GET /api/admin/databases", s.admin(s.adminDatabases))
	h("POST /api/admin/databases", s.admin(s.adminDatabaseCreate))
	h("GET /api/admin/databases/{id}", s.admin(s.adminDatabase))
	h("PUT /api/admin/databases/{id}", s.admin(s.adminDatabaseUpdate))
	h("DELETE /api/admin/databases/{id}", s.admin(s.adminDatabaseDelete))

	h("GET /", s.spa)

	return observe(securityHeaders(s.loadSession(mux)))
}


type ctxKey int

const (
	ctxPrincipal ctxKey = iota
	ctxSession
	ctxToken
	ctxInfo
)

// reqInfo is filled by inner handlers and read by the observe middleware.
type reqInfo struct{ route, actor string }

func infoOf(r *http.Request) *reqInfo {
	i, _ := r.Context().Value(ctxInfo).(*reqInfo)
	return i
}

func principal(r *http.Request) *authz.Principal {
	p, _ := r.Context().Value(ctxPrincipal).(*authz.Principal)
	return p
}

func currentSession(r *http.Request) *session.Session {
	s, _ := r.Context().Value(ctxSession).(*session.Session)
	return s
}


type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// observe starts the request span and writes one access log line per API request.
func observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := obs.Tracer.Start(ctx, r.Method+" "+r.URL.Path, trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.method", r.Method)))
		defer span.End()
		info := &reqInfo{route: "unmatched"}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r.WithContext(context.WithValue(ctx, ctxInfo, info)))
		took := time.Since(start)

		span.SetAttributes(attribute.String("http.route", info.route), attribute.Int("http.status_code", rec.status))
		level := slog.LevelInfo
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			level = slog.LevelDebug
		} else if rec.status >= 500 {
			level = slog.LevelError
		}
		slog.Log(ctx, level, "http request", "event", "http.request", "method", r.Method, "route", info.route,
			"status", rec.status, "duration_ms", took.Milliseconds(), "actor", info.actor)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		if !strings.HasPrefix(r.URL.Path, "/assets/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		c, err := r.Cookie(sessionCookie)
		if err != nil || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		p, sess, err := s.Auth.Authenticate(r.Context(), c.Value)
		if err != nil {
			if !errors.Is(err, session.ErrInvalid) {
				slog.ErrorContext(r.Context(), "session validation failed", "event", "session.error", "error", err)
			}
			s.clearCookie(w)
			next.ServeHTTP(w, r)
			return
		}
		token := c.Value
		if sess.NewToken != "" { // rotated: same cookie name, new value
			token = sess.NewToken
			s.setSessionCookie(w, token, sess.AbsoluteExpiresAt)
		}
		if info := infoOf(r); info != nil {
			info.actor = p.Username
		}
		ctx := context.WithValue(r.Context(), ctxPrincipal, p)
		ctx = context.WithValue(ctx, ctxSession, sess)
		ctx = context.WithValue(ctx, ctxToken, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authed requires a session; state-changing requests also need the session's
// CSRF token in the X-CSRF-Token header and a same-origin request.
func (s *Server) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if principal(r) == nil {
			writeError(w, http.StatusUnauthorized, "Sesi tidak valid atau telah berakhir. Silakan login kembali.")
			return
		}
		if r.Method != http.MethodGet && (!sameOrigin(r) || !validCSRF(r, currentSession(r).CSRFToken)) {
			writeError(w, http.StatusForbidden, "Permintaan ditolak: token CSRF tidak valid. Muat ulang halaman lalu coba lagi.")
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
	got := r.Header.Get(csrfHeader)
	return got != "" && expected != "" && subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

// sameOrigin rejects cross-site browser requests (Origin or Fetch Metadata).
func sameOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && origin != "null" && strings.EqualFold(u.Host, r.Host)
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", MaxAge: int(time.Until(expires).Seconds()),
		HttpOnly: true, Secure: s.CookieSecure, SameSite: http.SameSiteLaxMode})
}

func (s *Server) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.CookieSecure, SameSite: http.SameSiteLaxMode})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeOK(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}

// decode reads a JSON body, it only accepts application/json, which a
// cross-site form cannot send without a CORS preflight.
func decode(r *http.Request, v any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return model.Invalid("Content-Type harus application/json.")
	}
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return model.Invalid("Body JSON tidak valid.")
	}
	return nil
}

// fail maps an error to a status code and a message that is safe to show.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ve *model.ValidationError
	var he *workflow.HeldError
	switch {
	case errors.Is(err, authz.ErrForbidden):
		writeError(w, http.StatusForbidden, "Akses ditolak: Anda tidak memiliki hak akses untuk tindakan ini.")
	case errors.Is(err, authz.ErrUnauthenticated):
		writeError(w, http.StatusUnauthorized, "Silakan login terlebih dahulu.")
	case errors.Is(err, model.ErrNotFound), targetdb.PgCode(err) == "42P01":
		writeError(w, http.StatusNotFound, "Data yang diminta tidak ditemukan.")
	case errors.Is(err, model.ErrConflict):
		writeError(w, http.StatusConflict, "Nama sudah dipakai.")
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, ve.Msg)
	case errors.As(err, &he):
		writeError(w, http.StatusConflict, he.Msg)
	case targetdb.IsConnError(err):
		writeError(w, http.StatusBadGateway, "Tidak dapat terhubung ke database target. Periksa konfigurasi koneksi atau hubungi Platform Admin. Detail: "+err.Error())
	case targetdb.PgCode(err) == "42501":
		writeError(w, http.StatusForbidden, "Database target menolak perintah karena role koneksi tidak memiliki hak: "+targetdb.PgErrorMessage(err))
	case targetdb.PgCode(err) != "":
		writeError(w, http.StatusBadGateway, "Database target mengembalikan error: "+targetdb.PgErrorMessage(err))
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "Permintaan ke database melebihi batas waktu.")
	default:
		slog.ErrorContext(r.Context(), "request failed", "event", "http.error", "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, "Terjadi kesalahan pada server.")
	}
}

func pathID(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id
}
