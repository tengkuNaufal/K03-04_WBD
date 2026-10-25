package auth

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"dbmp/internal/authz"
	"dbmp/internal/model"
	"dbmp/internal/session"
)

var (
	ErrInvalidCredentials = errors.New("username atau password salah")
	ErrInvalidUsername    = errors.New("username 3-32 karakter: huruf kecil, angka, titik, garis bawah, atau strip")
)

var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{2,31}$`)

func ValidateUsername(u string) error {
	if !usernameRe.MatchString(u) {
		return ErrInvalidUsername
	}
	return nil
}

type UserStore interface {
	GetUserByUsername(ctx context.Context, username string) (model.User, error)
	GetUserByID(ctx context.Context, id int64) (model.User, error)
}

type Service struct {
	users    UserStore
	sessions *session.Manager
}

func NewService(users UserStore, sessions *session.Manager) *Service {
	return &Service{users: users, sessions: sessions}
}

func NormalizeUsername(u string) string { return strings.ToLower(strings.TrimSpace(u)) }

func (s *Service) Login(ctx context.Context, username, password, previousToken string) (string, *session.Session, error) {
	u, err := s.users.GetUserByUsername(ctx, NormalizeUsername(username))
	if errors.Is(err, model.ErrNotFound) {
		dummyVerify(password)
		return "", nil, ErrInvalidCredentials
	}
	if err != nil {
		return "", nil, err
	}
	if !VerifyPassword(u.PasswordHash, password) || !u.IsActive {
		return "", nil, ErrInvalidCredentials
	}
	if previousToken != "" {
		_ = s.sessions.Revoke(ctx, previousToken)
	}
	return s.sessions.Create(ctx, u.ID)
}

func (s *Service) Authenticate(ctx context.Context, token string) (*authz.Principal, *session.Session, error) {
	sess, err := s.sessions.Validate(ctx, token)
	if err != nil {
		return nil, nil, err
	}
	u, err := s.users.GetUserByID(ctx, sess.UserID)
	if errors.Is(err, model.ErrNotFound) || (err == nil && !u.IsActive) {
		_ = s.sessions.RevokeUser(ctx, sess.UserID)
		return nil, nil, session.ErrInvalid
	}
	if err != nil {
		return nil, nil, err
	}
	return &authz.Principal{UserID: u.ID, Username: u.Username, IsAdmin: u.IsAdmin}, sess, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	return s.sessions.Revoke(ctx, token)
}
