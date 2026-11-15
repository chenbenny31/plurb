package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"time"
)

// CreateSession issues a new random token for the user. Only the token's
// SHA-256 is stored; the raw token goes to the client cookie.
func (s *Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration) (token string, expires time.Time, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	expires = time.Now().Add(ttl)
	sum := sha256.Sum256([]byte(token))
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		sum[:], userID, expires)
	return token, expires, err
}

// UserBySession returns the user owning an unexpired session token.
func (s *Store) UserBySession(ctx context.Context, token string) (*User, error) {
	sum := sha256.Sum256([]byte(token))
	return scanUser(s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.display_name, u.password_hash, u.about_md, u.role, u.created_at, u.reputation
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`, sum[:]))
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	sum := sha256.Sum256([]byte(token))
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = $1`, sum[:])
	return err
}

// DeleteUserSessions signs a user out everywhere (after a password change).
func (s *Store) DeleteUserSessions(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	return err
}

func (s *Store) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
