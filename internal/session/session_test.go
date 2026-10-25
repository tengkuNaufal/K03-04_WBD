package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }
func testConfig() Config {
	return Config{IdleTimeout: 30 * time.Minute, AbsoluteTimeout: 8 * time.Hour, RotationInterval: 15 * time.Minute, GracePeriod: 30 * time.Second}
}
func newTestManager() (*Manager, *MemoryStore, *clock) {
	st := NewMemoryStore()
	m := NewManager(st, testConfig())
	c := &clock{t: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
	m.Now = c.now
	return m, st, c
}

var ctx = context.Background()

func TestCreateAndValidate(t *testing.T) {
	m, _, _ := newTestManager()
	tok, s, err := m.Create(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) < 40 {
		t.Fatalf("token too short to be unguessable: %d chars", len(tok))
	}
	if s.CSRFToken == "" || s.CSRFToken == tok {
		t.Fatal("csrf token must be set and differ from the session id")
	}
	got, err := m.Validate(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != 42 || got.NewToken != "" {
		t.Fatalf("unexpected session %+v", got)
	}
}

func TestTokensAreRandomAndStoredHashed(t *testing.T) {
	m, st, _ := newTestManager()
	a, _, _ := m.Create(ctx, 1)
	b, _, _ := m.Create(ctx, 1)
	if a == b {
		t.Fatal("two logins produced the same identifier")
	}
	for k := range st.tokens {
		if strings.Contains(k, a) || strings.Contains(k, b) {
			t.Fatal("raw identifier found in store")
		}
	}
}

func TestUnknownTokenIsInvalid(t *testing.T) {
	m, _, _ := newTestManager()
	if _, err := m.Validate(ctx, "not-a-real-token"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if _, err := m.Validate(ctx, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid for empty token, got %v", err)
	}
}

func TestIdleTimeout(t *testing.T) {
	m, _, c := newTestManager()
	tok, _, _ := m.Create(ctx, 1)
	c.advance(29 * time.Minute)
	if _, err := m.Validate(ctx, tok); err != nil {
		t.Fatalf("still within idle window: %v", err)
	}
	c.advance(31 * time.Minute)
	if _, err := m.Validate(ctx, tok); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want idle expiry, got %v", err)
	}
}

func TestAbsoluteExpirationDespiteActivity(t *testing.T) {
	m, _, c := newTestManager()
	tok, _, _ := m.Create(ctx, 1)
	for i := 0; i < 40; i++ { // 40 * 12 min = 8 hours of constant activity
		c.advance(12 * time.Minute)
		s, err := m.Validate(ctx, tok)
		if i < 39 && err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if err == nil && s.NewToken != "" {
			tok = s.NewToken
		}
		if i == 39 && !errors.Is(err, ErrInvalid) {
			t.Fatalf("want absolute expiry after 8h, got %v", err)
		}
	}
}

func TestRotationAndGracePeriod(t *testing.T) {
	m, _, c := newTestManager()
	old, first, _ := m.Create(ctx, 1)

	c.advance(16 * time.Minute)
	s, err := m.Validate(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	if s.NewToken == "" || s.NewToken == old {
		t.Fatal("identifier should rotate after the rotation interval")
	}
	if !s.AbsoluteExpiresAt.Equal(first.AbsoluteExpiresAt) {
		t.Fatal("rotation must not extend absolute expiration")
	}
	if s.CSRFToken != first.CSRFToken {
		t.Fatal("csrf token belongs to the login and survives rotation")
	}
	newTok := s.NewToken

	c.advance(10 * time.Second)
	s2, err := m.Validate(ctx, old)
	if err != nil {
		t.Fatalf("old id within grace period: %v", err)
	}
	if s2.NewToken != "" {
		t.Fatal("superseded identifier must not rotate again")
	}

	c.advance(30 * time.Second)
	if _, err := m.Validate(ctx, old); !errors.Is(err, ErrInvalid) {
		t.Fatalf("old id after grace period: want ErrInvalid, got %v", err)
	}
	if _, err := m.Validate(ctx, newTok); err != nil {
		t.Fatalf("new id should work: %v", err)
	}
}

func TestConcurrentRotationIssuesOneNewToken(t *testing.T) {
	m, st, c := newTestManager()
	tok, _, _ := m.Create(ctx, 1)
	c.advance(16 * time.Minute)
	f, tk, _ := st.Lookup(ctx, HashToken(tok))
	// Simulate another request rotating first.
	ok, _ := st.Rotate(ctx, f.ID, tk.Hash, Token{Hash: HashToken("other"), FamilyID: f.ID, IssuedAt: c.t}, c.t)
	if !ok {
		t.Fatal("first rotation should succeed")
	}
	s, err := m.Validate(ctx, tok)
	if err != nil {
		t.Fatalf("loser of the race is within grace period: %v", err)
	}
	if s.NewToken != "" {
		t.Fatal("loser of the race must not issue another identifier")
	}
}

func TestRevokeEndsWholeFamily(t *testing.T) {
	m, _, c := newTestManager()
	old, _, _ := m.Create(ctx, 1)
	c.advance(16 * time.Minute)
	s, _ := m.Validate(ctx, old)
	if err := m.Revoke(ctx, s.NewToken); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{old, s.NewToken} {
		if _, err := m.Validate(ctx, tok); !errors.Is(err, ErrInvalid) {
			t.Fatalf("identifier still valid after logout: %v", err)
		}
	}
}

func TestRevokeUserEndsAllSessions(t *testing.T) {
	m, _, _ := newTestManager()
	a, _, _ := m.Create(ctx, 7)
	b, _, _ := m.Create(ctx, 7)
	other, _, _ := m.Create(ctx, 8)
	if err := m.RevokeUser(ctx, 7); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{a, b} {
		if _, err := m.Validate(ctx, tok); !errors.Is(err, ErrInvalid) {
			t.Fatal("session of revoked user still valid")
		}
	}
	if _, err := m.Validate(ctx, other); err != nil {
		t.Fatal("other user's session must not be affected")
	}
}

func TestRequireRotationAfterPrivilegeIncrease(t *testing.T) {
	m, _, c := newTestManager()
	tok, _, _ := m.Create(ctx, 3)
	c.advance(time.Minute) // well before the rotation interval
	if err := m.RequireRotation(ctx, 3); err != nil {
		t.Fatal(err)
	}
	s, err := m.Validate(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if s.NewToken == "" {
		t.Fatal("privilege increase must rotate the identifier on next request")
	}
	s2, _ := m.Validate(ctx, s.NewToken)
	if s2.NewToken != "" {
		t.Fatal("rotation flag must be cleared after rotating")
	}
}

func TestCleanupRemovesEndedSessions(t *testing.T) {
	m, st, c := newTestManager()
	a, _, _ := m.Create(ctx, 1)
	_, _, _ = m.Create(ctx, 2)
	_ = m.Revoke(ctx, a)
	c.advance(9 * time.Hour)
	n, err := m.Cleanup(ctx)
	if err != nil || n != 2 {
		t.Fatalf("cleanup removed %d (%v), want 2", n, err)
	}
	if len(st.tokens) != 0 {
		t.Fatal("tokens of removed families should be gone")
	}
}
