package session

import (
	"context"
	"encoding/hex"
	"sync"
	"time"
)

type MemoryStore struct {
	mu       sync.Mutex
	families map[string]*Family
	tokens   map[string]*Token
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{families: map[string]*Family{}, tokens: map[string]*Token{}}
}

func key(h []byte) string { return hex.EncodeToString(h) }

func (s *MemoryStore) CreateFamily(_ context.Context, f Family, t Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fc, tc := f, t
	s.families[f.ID] = &fc
	s.tokens[key(t.Hash)] = &tc
	return nil
}

func (s *MemoryStore) Lookup(_ context.Context, h []byte) (Family, Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[key(h)]
	if !ok {
		return Family{}, Token{}, ErrNotFound
	}
	f, ok := s.families[t.FamilyID]
	if !ok {
		return Family{}, Token{}, ErrNotFound
	}
	return *f, *t, nil
}

func (s *MemoryStore) Touch(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, ok := s.families[id]; ok {
		f.LastSeenAt = at
	}
	return nil
}

func (s *MemoryStore) Rotate(_ context.Context, id string, old []byte, nt Token, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[key(old)]
	if !ok || t.SupersededAt != nil {
		return false, nil
	}
	at2 := at
	t.SupersededAt = &at2
	ntc := nt
	s.tokens[key(nt.Hash)] = &ntc
	if f, ok := s.families[id]; ok {
		f.RotateRequired = false
	}
	return true, nil
}

func (s *MemoryStore) RevokeFamily(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, ok := s.families[id]; ok && f.RevokedAt == nil {
		at2 := at
		f.RevokedAt = &at2
	}
	return nil
}

func (s *MemoryStore) RevokeUser(_ context.Context, userID int64, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.families {
		if f.UserID == userID && f.RevokedAt == nil {
			at2 := at
			f.RevokedAt = &at2
		}
	}
	return nil
}

func (s *MemoryStore) MarkRotateRequired(_ context.Context, userID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.families {
		if f.UserID == userID {
			f.RotateRequired = true
		}
	}
	return nil
}

func (s *MemoryStore) DeleteExpired(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for id, f := range s.families {
		if f.RevokedAt != nil || !before.Before(f.AbsoluteExpiresAt) {
			delete(s.families, id)
			n++
		}
	}
	for k, t := range s.tokens {
		if _, ok := s.families[t.FamilyID]; !ok {
			delete(s.tokens, k)
		}
	}
	return n, nil
}
