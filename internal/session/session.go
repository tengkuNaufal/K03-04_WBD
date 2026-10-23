package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"
)

type Config struct {
	IdleTimeout      time.Duration 
	AbsoluteTimeout  time.Duration 
	RotationInterval time.Duration 
	GracePeriod      time.Duration
}

func DefaultConfig() Config {
	return Config{
		IdleTimeout:      30 * time.Minute,
		AbsoluteTimeout:  8 * time.Hour,
		RotationInterval: 15 * time.Minute,
		GracePeriod:      30 * time.Second,
	}
}

var (
	ErrInvalid  = errors.New("session: invalid or expired")
	ErrNotFound = errors.New("session: not found")
)

type Family struct {
	ID                string
	UserID            int64
	CSRFToken         string
	CreatedAt         time.Time
	LastSeenAt        time.Time
	AbsoluteExpiresAt time.Time
	RevokedAt         *time.Time
	RotateRequired    bool
}

type Token struct {
	Hash         []byte
	FamilyID     string
	IssuedAt     time.Time
	SupersededAt *time.Time
}

type Store interface {
	CreateFamily(ctx context.Context, f Family, t Token) error
	Lookup(ctx context.Context, tokenHash []byte) (Family, Token, error)
	Touch(ctx context.Context, familyID string, at time.Time) error
	Rotate(ctx context.Context, familyID string, oldHash []byte, newToken Token, at time.Time) (bool, error)
	RevokeFamily(ctx context.Context, familyID string, at time.Time) error
	RevokeUser(ctx context.Context, userID int64, at time.Time) error
	MarkRotateRequired(ctx context.Context, userID int64) error
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}

type Session struct {
	FamilyID          string
	UserID            int64
	CSRFToken         string
	AbsoluteExpiresAt time.Time
	NewToken string
}

type Manager struct {
	store  Store
	cfg    Config
	Now    func() time.Time
	Random io.Reader
}

func NewManager(store Store, cfg Config) *Manager {
	return &Manager{store: store, cfg: cfg, Now: time.Now, Random: rand.Reader}
}

func (m *Manager) Config() Config { return m.cfg }

func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func (m *Manager) randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(m.Random, b); err != nil {
		return "", fmt.Errorf("session: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (m *Manager) Create(ctx context.Context, userID int64) (string, *Session, error) {
	now := m.Now()
	token, err := m.randomString(32)
	if err != nil {
		return "", nil, err
	}
	familyID, err := m.randomString(16)
	if err != nil {
		return "", nil, err
	}
	csrf, err := m.randomString(32)
	if err != nil {
		return "", nil, err
	}
	f := Family{
		ID:                familyID,
		UserID:            userID,
		CSRFToken:         csrf,
		CreatedAt:         now,
		LastSeenAt:        now,
		AbsoluteExpiresAt: now.Add(m.cfg.AbsoluteTimeout),
	}
	t := Token{Hash: HashToken(token), FamilyID: familyID, IssuedAt: now}
	if err := m.store.CreateFamily(ctx, f, t); err != nil {
		return "", nil, fmt.Errorf("session: create: %w", err)
	}
	return token, &Session{
		FamilyID:          familyID,
		UserID:            userID,
		CSRFToken:         csrf,
		AbsoluteExpiresAt: f.AbsoluteExpiresAt,
	}, nil
}

// Validate checks an identifier and refreshes the idle clock. It rotates the
// identifier when it is older than RotationInterval or when a rotation was
// requested (for example after a privilege increase).
func (m *Manager) Validate(ctx context.Context, token string) (*Session, error) {
	if token == "" {
		return nil, ErrInvalid
	}
	now := m.Now()
	f, t, err := m.store.Lookup(ctx, HashToken(token))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("session: lookup: %w", err)
	}
	if f.RevokedAt != nil {
		return nil, ErrInvalid
	}
	if !now.Before(f.AbsoluteExpiresAt) || now.Sub(f.LastSeenAt) >= m.cfg.IdleTimeout {
		_ = m.store.RevokeFamily(ctx, f.ID, now)
		return nil, ErrInvalid
	}
	if t.SupersededAt != nil && now.Sub(*t.SupersededAt) > m.cfg.GracePeriod {
		return nil, ErrInvalid
	}

	s := &Session{
		FamilyID:          f.ID,
		UserID:            f.UserID,
		CSRFToken:         f.CSRFToken,
		AbsoluteExpiresAt: f.AbsoluteExpiresAt,
	}

	if t.SupersededAt == nil && (f.RotateRequired || now.Sub(t.IssuedAt) >= m.cfg.RotationInterval) {
		newToken, err := m.randomString(32)
		if err != nil {
			return nil, err
		}
		ok, err := m.store.Rotate(ctx, f.ID, t.Hash, Token{Hash: HashToken(newToken), FamilyID: f.ID, IssuedAt: now}, now)
		if err != nil {
			return nil, fmt.Errorf("session: rotate: %w", err)
		}
		if ok {
			s.NewToken = newToken
		}
	}

	if err := m.store.Touch(ctx, f.ID, now); err != nil {
		return nil, fmt.Errorf("session: touch: %w", err)
	}
	return s, nil
}

func (m *Manager) Rotate(ctx context.Context, token string) (string, error) {
	now := m.Now()
	f, t, err := m.store.Lookup(ctx, HashToken(token))
	if err != nil {
		return "", ErrInvalid
	}
	newToken, err := m.randomString(32)
	if err != nil {
		return "", err
	}
	ok, err := m.store.Rotate(ctx, f.ID, t.Hash, Token{Hash: HashToken(newToken), FamilyID: f.ID, IssuedAt: now}, now)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrInvalid
	}
	return newToken, nil
}

func (m *Manager) Revoke(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	f, _, err := m.store.Lookup(ctx, HashToken(token))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return m.store.RevokeFamily(ctx, f.ID, m.Now())
}

func (m *Manager) RevokeUser(ctx context.Context, userID int64) error {
	return m.store.RevokeUser(ctx, userID, m.Now())
}

func (m *Manager) RequireRotation(ctx context.Context, userID int64) error {
	return m.store.MarkRotateRequired(ctx, userID)
}

func (m *Manager) Cleanup(ctx context.Context) (int64, error) {
	return m.store.DeleteExpired(ctx, m.Now())
}
