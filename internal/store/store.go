// Package store is the data-access layer: plain SQL over database/sql.
package store

import (
	"database/sql"
	"errors"

	"github.com/lib/pq"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrConflict means a unique constraint was violated (taken username, slug...).
	ErrConflict = errors.New("already exists")
)

type Store struct {
	db            *sql.DB
	notifyChannel string
}

func New(db *sql.DB) *Store { return &Store{db: db} }

// DB exposes the pool for callers that need their own transactions.
func (s *Store) DB() *sql.DB { return s.db }

func isUniqueViolation(err error) bool {
	var pe *pq.Error
	return errors.As(err, &pe) && pe.Code == "23505"
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
