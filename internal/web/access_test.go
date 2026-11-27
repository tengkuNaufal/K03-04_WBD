package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"golang.org/x/crypto/bcrypt"

	"dbmp/internal/auth"
	"dbmp/internal/authz"
	"dbmp/internal/model"
	"dbmp/internal/session"
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
		Assets:   fstest.MapFS{"index.html": {Data: []byte("<div id=root></div>")}},
	}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	return s, sessions
}

// request sends a JSON request as userID (0 = anonymous). State-changing
// requests carry the session's CSRF token unless csrf is false.
func request(t *testing.T, s *Server, sm *session.Manager, userID int64, method, path string, csrf bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	if userID != 0 {
		tok, sess, err := sm.Create(context.Background(), userID)
		if err != nil {
			t.Fatal(err)
		}
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: tok})
		if csrf {
			r.Header.Set(csrfHeader, sess.CSRFToken)
		}
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

// TestAccessRules: every protected action is refused in the backend before
// any data is touched (Store is nil here, so reaching it would panic).
func TestAccessRules(t *testing.T) {
	s, sm := newTestServer(t)
	cases := []struct {
		name   string
		user   int64
		method string
		path   string
		want   int
	}{
		{"anonymous read", 0, "GET", "/api/admin/users", http.StatusUnauthorized},
		{"anonymous write", 0, "POST", "/api/admin/groups", http.StatusUnauthorized},
		{"non-admin reads admin data", viewerID, "GET", "/api/admin/users", http.StatusForbidden},
		{"non-admin creates user", editorID, "POST", "/api/admin/users", http.StatusForbidden},
		{"non-admin adds connection", editorID, "POST", "/api/admin/databases", http.StatusForbidden},
		{"non-admin sets leader", editorID, "PUT", "/api/admin/groups/1/leader", http.StatusForbidden},
		{"non-admin grants access", editorID, "PUT", "/api/admin/groups/1/grants", http.StatusForbidden},
		{"user without group views db", outsiderID, "GET", "/api/db/1/tables", http.StatusForbidden},
		{"user without group reads history", outsiderID, "GET", "/api/db/1/history", http.StatusForbidden},
		{"user without group approves", outsiderID, "POST", "/api/db/1/requests/1/approve", http.StatusForbidden},
		{"admin is not a db level", adminID, "GET", "/api/db/1/migrations", http.StatusForbidden},
		{"view only creates migration", viewerID, "POST", "/api/db/1/migrations", http.StatusForbidden},
		{"view only edits migration", viewerID, "PUT", "/api/db/1/migrations/1", http.StatusForbidden},
		{"view only deletes migration", viewerID, "DELETE", "/api/db/1/migrations/1", http.StatusForbidden},
		{"view only validates", viewerID, "POST", "/api/db/1/migrations/1/validate", http.StatusForbidden},
		{"view only submits change", viewerID, "POST", "/api/db/1/migrations/1/requests", http.StatusForbidden},
		{"view only executes", viewerID, "POST", "/api/db/1/requests/1/execute", http.StatusForbidden},
		{"view only cancels", viewerID, "POST", "/api/db/1/requests/1/cancel", http.StatusForbidden},
		{"unknown api path", viewerID, "GET", "/api/nope", http.StatusNotFound},
	}
	for _, c := range cases {
		if w := request(t, s, sm, c.user, c.method, c.path, true); w.Code != c.want {
			t.Errorf("%s: status %d, want %d (%s)", c.name, w.Code, c.want, w.Body.String())
		}
	}
}

func TestWriteWithoutCSRFIsRejected(t *testing.T) {
	s, sm := newTestServer(t)
	if w := request(t, s, sm, adminID, "POST", "/api/admin/groups", false); w.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", w.Code)
	}
}

func TestLoginCookieAndLogout(t *testing.T) {
	s, sm := newTestServer(t)
	r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"username":"vera","password":"password123"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("login status %d %s", w.Code, w.Body.String())
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
	if strings.Contains(w.Body.String(), sc.Value) {
		t.Fatal("session id leaked into response")
	}

	// Logout ends the session in the backend.
	sess, err := sm.Validate(context.Background(), sc.Value)
	if err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("POST", "/api/logout", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(csrfHeader, sess.CSRFToken)
	r.AddCookie(sc)
	s.ServeHTTP(httptest.NewRecorder(), r)
	if _, err := sm.Validate(context.Background(), sc.Value); err == nil {
		t.Fatal("session still valid after logout")
	}
}

func TestSPAFallback(t *testing.T) {
	s, _ := newTestServer(t)
	for _, p := range []string{"/", "/db/1/migrations", "/admin/users"} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "root") {
			t.Errorf("%s: status %d", p, w.Code)
		}
	}
}
