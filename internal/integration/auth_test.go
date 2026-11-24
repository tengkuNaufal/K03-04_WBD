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
	expectStatus(t, "login", r, http.StatusOK)

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
	if strings.Contains(r.body, sc.Value) || strings.Contains(r.body, "password-root") {
		t.Fatal("session identifier or password leaked into the response body")
	}
	s := c.get("/api/session")
	expectStatus(t, "session", s, http.StatusOK)
	if !strings.Contains(s.body, `"username":"root"`) || strings.Contains(s.body, sc.Value) {
		t.Fatalf("session endpoint: %s", s.body)
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
	// A cross-site page cannot log a victim in (login CSRF).
	r = c.do("POST", "/api/login", map[string]string{"username": "root", "password": "password-root"},
		map[string]string{"Origin": "https://evil.example"})
	expectStatus(t, "cross-site login", r, http.StatusForbidden)
	// Plain form posts are not accepted.
	r = c.do("POST", "/api/login", nil, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	expectStatus(t, "form login", r, http.StatusBadRequest)
}

func TestProtectedEndpointsRequireLogin(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	for _, p := range []string{"/api/session", "/api/databases", "/api/admin/users", "/api/db/1", "/api/db/1/migrations"} {
		expectStatus(t, p, c.get(p), http.StatusUnauthorized)
	}
	expectStatus(t, "anonymous post", c.post("/api/admin/groups", map[string]string{"name": "x"}), http.StatusUnauthorized)
	// The SPA itself loads for everyone; it then asks /api/session.
	expectStatus(t, "spa", c.get("/db/1/migrations"), http.StatusOK)
}

func TestLogoutRevokesSessionOnServer(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	c.mustLogin("root")
	old := c.sessionCookie()
	expectStatus(t, "logout", c.post("/api/logout", nil), http.StatusOK)

	// Replaying the old identifier must not work: the session is dead on
	// the server, not only removed from the browser.
	replay := e.client()
	u, _ := url.Parse(e.srv.URL)
	replay.http.Jar.SetCookies(u, []*http.Cookie{{Name: "dbmp_session", Value: old}})
	expectStatus(t, "replay", replay.get("/api/session"), http.StatusUnauthorized)
}

func TestStateChangesRequireCSRF(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	c.mustLogin("root")
	body := map[string]string{"name": "g"}

	expectStatus(t, "missing token", c.do("POST", "/api/admin/groups", body, nil), http.StatusForbidden)
	expectStatus(t, "wrong token", c.do("POST", "/api/admin/groups", body, map[string]string{"X-CSRF-Token": "forged"}), http.StatusForbidden)
	expectStatus(t, "cross origin", c.do("POST", "/api/admin/groups", body,
		map[string]string{"X-CSRF-Token": c.csrf, "Origin": "https://evil.example"}), http.StatusForbidden)
	expectStatus(t, "cross-site fetch", c.do("POST", "/api/admin/groups", body,
		map[string]string{"X-CSRF-Token": c.csrf, "Sec-Fetch-Site": "cross-site"}), http.StatusForbidden)

	if groups, _ := e.store.ListGroups(context.Background()); len(groups) != 0 {
		t.Fatalf("rejected requests created groups: %v", groups)
	}
	expectStatus(t, "valid token", c.post("/api/admin/groups", body), http.StatusCreated)
}

func TestDeactivatedUserIsLoggedOut(t *testing.T) {
	e := newEnv(t)
	uid := e.createUser("alice", false)
	alice := e.client()
	alice.mustLogin("alice")
	expectStatus(t, "alice", alice.get("/api/session"), http.StatusOK)

	root := e.client()
	root.mustLogin("root")
	r := root.send("PUT", fmt.Sprintf("/api/admin/users/%d", uid), map[string]bool{"is_admin": false, "is_active": false})
	expectStatus(t, "deactivate", r, http.StatusOK)

	expectStatus(t, "after deactivation", alice.get("/api/session"), http.StatusUnauthorized)
	expectStatus(t, "login while inactive", alice.login("alice", "password-alice"), http.StatusUnauthorized)
}

func TestNonAdminCannotUseAdminEndpoints(t *testing.T) {
	e := newEnv(t)
	e.createUser("alice", false)
	c := e.client()
	c.mustLogin("alice")
	for _, p := range []string{"/api/admin/users", "/api/admin/groups", "/api/admin/databases"} {
		expectStatus(t, p, c.get(p), http.StatusForbidden)
	}
	r := c.post("/api/admin/users", map[string]any{"username": "mallory", "password": "password123", "is_admin": true})
	expectStatus(t, "create user", r, http.StatusForbidden)
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
	expectStatus(t, "promote", root.send("PUT", fmt.Sprintf("/api/admin/users/%d", uid),
		map[string]bool{"is_admin": true, "is_active": true}), http.StatusOK)

	expectStatus(t, "admin endpoint after promotion", alice.get("/api/admin/users"), http.StatusOK)
	if after := alice.sessionCookie(); after == "" || after == before {
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
