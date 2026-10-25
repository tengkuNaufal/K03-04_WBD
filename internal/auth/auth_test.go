package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"dbmp/internal/model"
	"dbmp/internal/session"
)

func init() { Cost = bcrypt.MinCost }

func TestHashPasswordIsNotPlaintext(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h, "correct horse") || !strings.HasPrefix(h, "$2") {
		t.Fatalf("unexpected hash %q", h)
	}
	h2, _ := HashPassword("correct horse")
	if h == h2 {
		t.Fatal("hashes must be salted")
	}
	if !VerifyPassword(h, "correct horse") {
		t.Fatal("correct password rejected")
	}
	if VerifyPassword(h, "wrong horse") {
		t.Fatal("wrong password accepted")
	}
}

func TestValidatePassword(t *testing.T) {
	if !errors.Is(ValidatePassword("short"), ErrPasswordTooShort) {
		t.Fatal("short password accepted")
	}
	if !errors.Is(ValidatePassword(strings.Repeat("a", 73)), ErrPasswordTooLong) {
		t.Fatal("password over bcrypt limit accepted")
	}
	if ValidatePassword("long enough") != nil {
		t.Fatal("valid password rejected")
	}
}

type fakeUsers struct{ byName map[string]model.User }

func (f *fakeUsers) GetUserByUsername(_ context.Context, u string) (model.User, error) {
	if x, ok := f.byName[u]; ok {
		return x, nil
	}
	return model.User{}, model.ErrNotFound
}

func (f *fakeUsers) GetUserByID(_ context.Context, id int64) (model.User, error) {
	for _, u := range f.byName {
		if u.ID == id {
			return u, nil
		}
	}
	return model.User{}, model.ErrNotFound
}

func newService(t *testing.T) (*Service, *fakeUsers, *session.Manager) {
	t.Helper()
	hash, _ := HashPassword("password123")
	users := &fakeUsers{byName: map[string]model.User{
		"alice": {ID: 1, Username: "alice", PasswordHash: hash, IsActive: true},
		"bob":   {ID: 2, Username: "bob", PasswordHash: hash, IsActive: false},
	}}
	sm := session.NewManager(session.NewMemoryStore(), session.DefaultConfig())
	return NewService(users, sm), users, sm
}

func TestLogin(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()

	tok, sess, err := svc.Login(ctx, "  Alice ", "password123", "")
	if err != nil || tok == "" || sess.UserID != 1 {
		t.Fatalf("login failed: %v", err)
	}
	for _, c := range []struct{ user, pw string }{
		{"alice", "wrong-password"},
		{"nobody", "password123"},
		{"bob", "password123"}, // inactive
	} {
		if _, _, err := svc.Login(ctx, c.user, c.pw, ""); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s/%s: want ErrInvalidCredentials, got %v", c.user, c.pw, err)
		}
	}
}

func TestLoginRevokesPreviousSession(t *testing.T) {
	svc, _, sm := newService(t)
	ctx := context.Background()
	first, _, _ := svc.Login(ctx, "alice", "password123", "")
	second, _, _ := svc.Login(ctx, "alice", "password123", first)
	if first == second {
		t.Fatal("identifier must change at login")
	}
	if _, err := sm.Validate(ctx, first); !errors.Is(err, session.ErrInvalid) {
		t.Fatal("previous session must be revoked at login")
	}
}

func TestAuthenticate(t *testing.T) {
	svc, users, _ := newService(t)
	ctx := context.Background()
	tok, _, _ := svc.Login(ctx, "alice", "password123", "")

	p, _, err := svc.Authenticate(ctx, tok)
	if err != nil || p.UserID != 1 || p.Username != "alice" || p.IsAdmin {
		t.Fatalf("unexpected principal %+v, %v", p, err)
	}

	// Admin flag is read on every request.
	u := users.byName["alice"]
	u.IsAdmin = true
	users.byName["alice"] = u
	p, _, _ = svc.Authenticate(ctx, tok)
	if !p.IsAdmin {
		t.Fatal("principal must reflect current user flags")
	}

	// Deactivated users lose their session.
	u.IsActive = false
	users.byName["alice"] = u
	if _, _, err := svc.Authenticate(ctx, tok); !errors.Is(err, session.ErrInvalid) {
		t.Fatalf("inactive user authenticated: %v", err)
	}
	u.IsActive = true
	users.byName["alice"] = u
	if _, _, err := svc.Authenticate(ctx, tok); !errors.Is(err, session.ErrInvalid) {
		t.Fatal("session must stay revoked after reactivation")
	}
}

func TestLogout(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	tok, _, _ := svc.Login(ctx, "alice", "password123", "")
	if err := svc.Logout(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Authenticate(ctx, tok); !errors.Is(err, session.ErrInvalid) {
		t.Fatal("session valid after logout")
	}
}
