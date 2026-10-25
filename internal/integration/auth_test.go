package integration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"dbmp/internal/session"
)

func TestLoginSetsSecureSessionCookie(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	r := c.login("root", "password-root")
	expectStatus(t, "login", r, http.StatusSeeOther)

	var sc *http.Cookie
	for _, ck := range r.cookies {
		if ck.Name == "dbmp_session" {
			sc = ck
		}
	}
	if sc == nil || sc.Value == "" {
		t.Fatal("no session cookie")
	}
	if !sc.HttpOnly || sc.SameSite != http.SameSiteLaxMode || sc.Path != "/" {
		t.Fatalf("cookie flags: %+v", sc)
	}
	if strings.Contains(r.location, sc.Value) || strings.Contains(r.body, sc.Value) {
		t.Fatal("session identifier leaked into URL or body")
	}
	home := c.get("/")
	expectStatus(t, "dashboard", home, http.StatusOK)
	if strings.Contains(home.body, sc.Value) || strings.Contains(home.body, "password-root") {
		t.Fatal("secret leaked into page")
	}
	if home.status == http.StatusOK && !strings.Contains(home.body, "root") {
		t.Fatal("dashboard should show username")
	}
}

func TestLoginFailures(t *testing.T) {
	e := newEnv(t)
	c := e.client()

	r := c.login("root", "wrong-password")
	expectStatus(t, "wrong password", r, http.StatusUnauthorized)
	if strings.Contains(r.body, "wrong-password") {
		t.Fatal("password echoed back")
	}
	expectStatus(t, "unknown user", c.login("ghost", "whatever-123"), http.StatusUnauthorized)
	if c.sessionCookie() != "" {
		t.Fatal("failed login must not create a session")
	}

	// Login form without the CSRF token is rejected.
	r = c.postRaw("/login", url.Values{"username": {"root"}, "password": {"password-root"}}, nil)
	expectStatus(t, "login without csrf", r, http.StatusForbidden)
}

func TestProtectedPagesRequireLogin(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	for _, p := range []string{"/", "/admin/users", "/db/1", "/db/1/migrations"} {
		r := c.get(p)
		if r.status != http.StatusSeeOther || !strings.HasPrefix(r.location, "/login") {
			t.Errorf("%s: status %d location %q", p, r.status, r.location)
		}
	}
	r := c.postRaw("/admin/groups", url.Values{"name": {"x"}}, nil)
	expectStatus(t, "anonymous post", r, http.StatusUnauthorized)
}

func TestLogoutRevokesSessionOnServer(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	c.mustLogin("root")
	old := c.sessionCookie()

	r := c.post("/logout", nil)
	expectStatus(t, "logout", r, http.StatusSeeOther)

	// Replaying the old identifier must not work: the session is dead on
	// the server, not only removed from the browser.
	replay := e.client()
	u, _ := url.Parse(e.srv.URL)
	replay.http.Jar.SetCookies(u, []*http.Cookie{{Name: "dbmp_session", Value: old}})
	r = replay.get("/")
	if r.status != http.StatusSeeOther || !strings.HasPrefix(r.location, "/login") {
		t.Fatalf("old session still accepted: %d", r.status)
	}
}

func TestStateChangesRequireCSRF(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	c.mustLogin("root")

	r := c.postRaw("/admin/groups", url.Values{"name": {"no-token"}}, nil)
	expectStatus(t, "missing token", r, http.StatusForbidden)
	r = c.postRaw("/admin/groups", url.Values{"name": {"bad-token"}, "csrf_token": {"forged"}}, nil)
	expectStatus(t, "wrong token", r, http.StatusForbidden)
	token := c.csrf("/")
	r = c.postRaw("/admin/groups", url.Values{"name": {"cross-site"}, "csrf_token": {token}},
		map[string]string{"Origin": "https://evil.example"})
	expectStatus(t, "cross origin", r, http.StatusForbidden)

	groups, _ := e.store.ListGroups(context.Background())
	if len(groups) != 0 {
		t.Fatalf("rejected requests created groups: %v", groups)
	}
	r = c.post("/admin/groups", url.Values{"name": {"ok"}})
	expectStatus(t, "valid token", r, http.StatusSeeOther)
}

func TestDeactivatedUserIsLoggedOut(t *testing.T) {
	e := newEnv(t)
	uid := e.createUser("alice", false)
	alice := e.client()
	alice.mustLogin("alice")
	expectStatus(t, "alice home", alice.get("/"), http.StatusOK)

	root := e.client()
	root.mustLogin("root")
	r := root.post(fmt.Sprintf("/admin/users/%d", uid), url.Values{}) // is_active unchecked
	expectStatus(t, "deactivate", r, http.StatusSeeOther)

	r = alice.get("/")
	if r.status != http.StatusSeeOther || !strings.HasPrefix(r.location, "/login") {
		t.Fatalf("deactivated user still logged in: %d", r.status)
	}
	expectStatus(t, "login while inactive", alice.login("alice", "password-alice"), http.StatusUnauthorized)
}

func TestNonAdminCannotUseAdminPages(t *testing.T) {
	e := newEnv(t)
	e.createUser("alice", false)
	c := e.client()
	c.mustLogin("alice")
	for _, p := range []string{"/admin/users", "/admin/groups", "/admin/databases"} {
		expectStatus(t, p, c.get(p), http.StatusForbidden)
	}
	// Hidden UI is not the protection: direct POSTs are rejected too.
	expectStatus(t, "create user", c.post("/admin/users", url.Values{"username": {"mallory"}, "password": {"password123"}, "is_admin": {"1"}}), http.StatusForbidden)
	if _, err := e.store.GetUserByUsername(context.Background(), "mallory"); err == nil {
		t.Fatal("non-admin created a user")
	}
}

func TestPrivilegeIncreaseRotatesSessionIdentifier(t *testing.T) {
	e := newEnv(t)
	uid := e.createUser("alice", false)
	alice := e.client()
	alice.mustLogin("alice")
	before := alice.sessionCookie()

	root := e.client()
	root.mustLogin("root")
	expectStatus(t, "promote", root.post(fmt.Sprintf("/admin/users/%d", uid), url.Values{"is_admin": {"1"}, "is_active": {"1"}}), http.StatusSeeOther)

	expectStatus(t, "admin page after promotion", alice.get("/admin/users"), http.StatusOK)
	after := alice.sessionCookie()
	if after == "" || after == before {
		t.Fatal("identifier must change after privilege increase")
	}
}

// TestPostgresSessionStore runs the session lifecycle on the real store with
// a controlled clock.
func TestPostgresSessionStore(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	uid := e.createUser("alice", false)
	now := time.Now().UTC().Truncate(time.Microsecond)
	m := session.NewManager(e.store.Sessions(), session.Config{
		IdleTimeout: 30 * time.Minute, AbsoluteTimeout: 2 * time.Hour,
		RotationInterval: 15 * time.Minute, GracePeriod: 30 * time.Second,
	})
	m.Now = func() time.Time { return now }

	tok, _, err := m.Create(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(16 * time.Minute)
	s, err := m.Validate(ctx, tok)
	if err != nil || s.NewToken == "" {
		t.Fatalf("expected rotation: %+v %v", s, err)
	}
	now = now.Add(10 * time.Second)
	if _, err := m.Validate(ctx, tok); err != nil {
		t.Fatalf("grace period: %v", err)
	}
	now = now.Add(time.Minute)
	if _, err := m.Validate(ctx, tok); !errors.Is(err, session.ErrInvalid) {
		t.Fatal("old id after grace period accepted")
	}
	if _, err := m.Validate(ctx, s.NewToken); err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * time.Minute)
	if _, err := m.Validate(ctx, s.NewToken); !errors.Is(err, session.ErrInvalid) {
		t.Fatal("idle session accepted")
	}

	tok2, _, _ := m.Create(ctx, uid)
	for i := 0; i < 9; i++ { // 9 * 14 min > 2 h absolute lifetime
		now = now.Add(14 * time.Minute)
		s, err := m.Validate(ctx, tok2)
		if err != nil {
			if i < 8 {
				t.Fatalf("expired too early at step %d", i)
			}
			break
		}
		if s.NewToken != "" {
			tok2 = s.NewToken
		}
		if i == 8 {
			t.Fatal("absolute expiration not enforced")
		}
	}

	n, err := m.Cleanup(ctx)
	if err != nil || n < 2 {
		t.Fatalf("cleanup: %d %v", n, err)
	}
}
