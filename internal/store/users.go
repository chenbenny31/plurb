package store

import (
	"context"
	"regexp"
	"time"
)

type User struct {
	ID           int64
	Username     string
	DisplayName  string
	PasswordHash string // empty for accounts that only sign in via OAuth
	AboutMD      string
	Role         string
	CreatedAt    time.Time
}

func (u *User) IsAdmin() bool { return u != nil && u.Role == "admin" }

// UsernamePattern mirrors the CHECK constraint on users.username.
var UsernamePattern = regexp.MustCompile(`^[a-z0-9_]{3,32}$`)

// reserved names collide with routes like /panorama/search or /panorama/editor.
var reserved = map[string]bool{
	"admin": true, "api": true, "auth": true, "editor": true, "feed": true,
	"forum": true, "login": true, "logout": true, "search": true, "signup": true,
	"static": true, "settings": true, "sitemap": true, "panorama": true, "plurb": true,
}

// ValidUsername reports whether name is well-formed and not reserved.
func ValidUsername(name string) bool { return UsernamePattern.MatchString(name) && !reserved[name] }

const userCols = `id, username, display_name, password_hash, about_md, role, created_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	u := &User{}
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.AboutMD, &u.Role, &u.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	return u, nil
}

func (s *Store) CreateUser(ctx context.Context, username, displayName, passwordHash, role string) (*User, error) {
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO users (username, display_name, password_hash, role)
		VALUES ($1, $2, $3, $4)
		RETURNING `+userCols, username, displayName, passwordHash, role)
	u, err := scanUser(row)
	if isUniqueViolation(err) {
		return nil, ErrConflict
	}
	return u, err
}

func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = $1`, id))
}

func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = $1`, username))
}

func (s *Store) UsernameTaken(ctx context.Context, username string) (bool, error) {
	var taken bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE username = $1)`, username).Scan(&taken)
	return taken, err
}

func (s *Store) UpdatePassword(ctx context.Context, userID int64, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1`, userID, hash)
	return err
}

func (s *Store) UpdateUsername(ctx context.Context, userID int64, username string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET username = $2 WHERE id = $1`, userID, username)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

func (s *Store) UpdateProfile(ctx context.Context, userID int64, displayName, aboutMD string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET display_name = $2, about_md = $3 WHERE id = $1`, userID, displayName, aboutMD)
	return err
}

// UserByIdentity finds the account linked to an external login, e.g.
// ("github", "12345").
func (s *Store) UserByIdentity(ctx context.Context, provider, subject string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.display_name, u.password_hash, u.about_md, u.role, u.created_at
		FROM user_identities i JOIN users u ON u.id = i.user_id
		WHERE i.provider = $1 AND i.subject = $2`, provider, subject))
}

func (s *Store) LinkIdentity(ctx context.Context, userID int64, provider, subject, login string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO user_identities (user_id, provider, subject, login)
		VALUES ($1, $2, $3, $4)`, userID, provider, subject, login)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

// CreateUserWithIdentity creates an OAuth-only account and links it in one
// transaction, so a failure can't leave an account nobody can sign in to.
func (s *Store) CreateUserWithIdentity(ctx context.Context, username, displayName, provider, subject, login string) (*User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	u, err := scanUser(tx.QueryRowContext(ctx, `
		INSERT INTO users (username, display_name, password_hash)
		VALUES ($1, $2, '')
		RETURNING `+userCols, username, displayName))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_identities (user_id, provider, subject, login)
		VALUES ($1, $2, $3, $4)`, u.ID, provider, subject, login); err != nil {
		if isUniqueViolation(err) {
			return nil, ErrConflict
		}
		return nil, err
	}
	return u, tx.Commit()
}

func (s *Store) HasIdentity(ctx context.Context, userID int64, provider string) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM user_identities WHERE user_id = $1 AND provider = $2)`, userID, provider).Scan(&ok)
	return ok, err
}
