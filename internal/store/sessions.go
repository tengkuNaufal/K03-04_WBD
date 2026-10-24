package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"dbmp/internal/session"
)

type SessionStore struct{ s *Store }

func (s *Store) Sessions() *SessionStore { return &SessionStore{s: s} }

func (ss *SessionStore) CreateFamily(ctx context.Context, f session.Family, t session.Token) error {
	tx, err := ss.s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) 
	if _, err := tx.Exec(ctx, `
		INSERT INTO session_families (id, user_id, csrf_token, created_at, last_seen_at, absolute_expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		f.ID, f.UserID, f.CSRFToken, f.CreatedAt, f.LastSeenAt, f.AbsoluteExpiresAt); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO session_tokens (token_hash, family_id, issued_at) VALUES ($1, $2, $3)`,
		t.Hash, t.FamilyID, t.IssuedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (ss *SessionStore) Lookup(ctx context.Context, hash []byte) (session.Family, session.Token, error) {
	var f session.Family
	var t session.Token
	err := ss.s.pool.QueryRow(ctx, `
		SELECT f.id, f.user_id, f.csrf_token, f.created_at, f.last_seen_at, f.absolute_expires_at,
		       f.revoked_at, f.rotate_required, t.token_hash, t.issued_at, t.superseded_at
		FROM session_tokens t JOIN session_families f ON f.id = t.family_id
		WHERE t.token_hash = $1`, hash).Scan(
		&f.ID, &f.UserID, &f.CSRFToken, &f.CreatedAt, &f.LastSeenAt, &f.AbsoluteExpiresAt,
		&f.RevokedAt, &f.RotateRequired, &t.Hash, &t.IssuedAt, &t.SupersededAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, t, session.ErrNotFound
	}
	t.FamilyID = f.ID
	return f, t, err
}

func (ss *SessionStore) Touch(ctx context.Context, familyID string, at time.Time) error {
	_, err := ss.s.pool.Exec(ctx,
		`UPDATE session_families SET last_seen_at = GREATEST(last_seen_at, $2) WHERE id = $1`, familyID, at)
	return err
}

func (ss *SessionStore) Rotate(ctx context.Context, familyID string, oldHash []byte, nt session.Token, at time.Time) (bool, error) {
	tx, err := ss.s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) 
	tag, err := tx.Exec(ctx, `
		UPDATE session_tokens SET superseded_at = $3
		WHERE token_hash = $1 AND family_id = $2 AND superseded_at IS NULL`, oldHash, familyID, at)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil 
	}
	if _, err := tx.Exec(ctx, `INSERT INTO session_tokens (token_hash, family_id, issued_at) VALUES ($1, $2, $3)`,
		nt.Hash, familyID, nt.IssuedAt); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE session_families SET rotate_required = FALSE WHERE id = $1`, familyID); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (ss *SessionStore) RevokeFamily(ctx context.Context, familyID string, at time.Time) error {
	_, err := ss.s.pool.Exec(ctx,
		`UPDATE session_families SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, familyID, at)
	return err
}

func (ss *SessionStore) RevokeUser(ctx context.Context, userID int64, at time.Time) error {
	_, err := ss.s.pool.Exec(ctx,
		`UPDATE session_families SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`, userID, at)
	return err
}

func (ss *SessionStore) MarkRotateRequired(ctx context.Context, userID int64) error {
	_, err := ss.s.pool.Exec(ctx,
		`UPDATE session_families SET rotate_required = TRUE WHERE user_id = $1 AND revoked_at IS NULL`, userID)
	return err
}

func (ss *SessionStore) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	tag, err := ss.s.pool.Exec(ctx,
		`DELETE FROM session_families WHERE revoked_at IS NOT NULL OR absolute_expires_at <= $1`, before)
	return tag.RowsAffected(), err
}
