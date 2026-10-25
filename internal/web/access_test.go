package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"dbmp/internal/auth"
	"dbmp/internal/authz"
	"dbmp/internal/model"
	"dbmp/internal/session"
	webui "dbmp/web"
)

type stubUsers map[int64]model.User

func (u stubUsers) GetUserByID(_ context.Context, id int64) (model.User, error) {
	if x, ok := u[id]; ok {
		return x, nil
	}
	return model.User{}, model.ErrNotFound
}

func (u stubUsers) GetUserByUsername(_ context.Context, name string) (model.User, error) {
	for _, x := range u {
		if x.Username == name {
			return x, nil
		}
	}
	return model.User{}, model.ErrNotFound
}

type stubLevels map[int64]authz.Level // user id -> level on every database

func (l stubLevels) GroupLevels(_ context.Context, userID, _ int64) ([]authz.Level, error) {
	if lvl, ok := l[userID]; ok {
		return []authz.Level{lvl}, nil
	}
	return nil, nil
}

const (
	adminID = iota + 1
	viewerID
	editorID
	outsiderID
)

func newTestServer(t *testing.T) (*Server, *session.Manager) {
	t.Helper()
	auth.Cost = bcrypt.MinCost
	hash, _ := auth.HashPassword("password123")
	users := stubUsers{
		adminID:    {ID: adminID, Username: "root", PasswordHash: hash, IsAdmin: true, IsActive: true},
		viewerID:   {ID: viewerID, Username: "vera", PasswordHash: hash, IsActive: true},
		editorID:   {ID: editorID, Username: "eddie", PasswordHash: hash, IsActive: true},
		outsiderID: {ID: outsiderID, Username: "otto", PasswordHash: hash, IsActive: true},
	}
	sessions := session.NewManager(session.NewMemoryStore(), session.DefaultConfig())
	s := &Server{
		Sessions: sessions,
		Auth:     auth.NewService(users, sessions),
		Policy:   authz.NewPolicy(stubLevels{viewerID: authz.LevelView, editorID: authz.LevelEdit}),
		Assets:   webui.FS,
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	return s, sessions
}

func request(t *testing.T, s *Server, sm *session.Manager, userID int64, method, path string, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{}
	var token string
	if userID != 0 {
		tok, sess, err := sm.Create(context.Background(), userID)
		if err != nil {
			t.Fatal(err)
		}
		token = tok
		if csrf {
			form.Set(csrfField, sess.CSRFToken)
		}
	}
	r := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if token != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestAccessRules(t *testing.T) {
	s, sm := newTestServer(t)
	cases := []struct {
		name   string
		user   int64
		method string
		path   string
		want   int
	}{
		{"anonymous page redirects to login", 0, "GET", "/admin/users", http.StatusSeeOther},
		{"anonymous post rejected", 0, "POST", "/admin/groups", http.StatusUnauthorized},
		{"non-admin cannot open admin page", viewerID, "GET", "/admin/users", http.StatusForbidden},
		{"non-admin cannot create user", editorID, "POST", "/admin/users", http.StatusForbidden},
		{"non-admin cannot add connection", editorID, "POST", "/admin/databases", http.StatusForbidden},
		{"non-admin cannot grant access", editorID, "POST", "/admin/groups/1/grants", http.StatusForbidden},
		{"user without group cannot view db", outsiderID, "GET", "/db/1", http.StatusForbidden},
		{"admin is not a db level", adminID, "GET", "/db/1/migrations", http.StatusForbidden},
		{"view only cannot open editor", viewerID, "GET", "/db/1/migrations/new", http.StatusForbidden},
		{"view only cannot create migration", viewerID, "POST", "/db/1/migrations", http.StatusForbidden},
		{"view only cannot edit migration", viewerID, "POST", "/db/1/migrations/1", http.StatusForbidden},
		{"view only cannot execute migration", viewerID, "POST", "/db/1/migrations/1/execute", http.StatusForbidden},
		{"view only cannot explain migration", viewerID, "POST", "/db/1/migrations/1/explain", http.StatusForbidden},
	}
	for _, c := range cases {
		if w := request(t, s, sm, c.user, c.method, c.path, true); w.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, w.Code, c.want)
		}
	}
}

func TestPostWithoutCSRFIsRejected(t *testing.T) {
	s, sm := newTestServer(t)
	if w := request(t, s, sm, adminID, "POST", "/admin/groups", false); w.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", w.Code)
	}
}

func TestLoginCookieAndLogout(t *testing.T) {
	s, sm := newTestServer(t)

	// Login form: CSRF token in the page and in a cookie (double submit).
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/login", nil))
	csrf := w.Result().Cookies()[0]
	form := url.Values{"username": {"vera"}, "password": {"password123"}, csrfField: {csrf.Value}}
	r := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(csrf)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("login status %d", w.Code)
	}
	var sc *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			sc = c
		}
	}
	if sc == nil || !sc.HttpOnly || sc.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie flags: %+v", sc)
	}
	if strings.Contains(w.Body.String(), sc.Value) || strings.Contains(w.Header().Get("Location"), sc.Value) {
		t.Fatal("session id leaked into response")
	}

	// Logout ends the session in the backend.
	sess, err := sm.Validate(context.Background(), sc.Value)
	if err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("POST", "/logout", strings.NewReader(url.Values{csrfField: {sess.CSRFToken}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(sc)
	s.ServeHTTP(httptest.NewRecorder(), r)
	if _, err := sm.Validate(context.Background(), sc.Value); err == nil {
		t.Fatal("session still valid after logout")
	}
}
