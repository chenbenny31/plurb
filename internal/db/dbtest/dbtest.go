// Package dbtest gives integration tests a freshly migrated database.
//
// Tests that use it are skipped unless PLURB_TEST_DATABASE_URL is set, e.g.
//
//	PLURB_TEST_DATABASE_URL='postgres://plurb:plurb@localhost:5432/plurb_test?sslmode=disable' go test ./...
//
// Each call gets its own throwaway schema, so tests (and packages, which
// `go test` runs in parallel) never see each other's rows. The database
// name must end in "_test" as a guard against pointing this at real data.
package dbtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/chenbenny31/plurb/internal/db"
)

// New returns a pool on a fresh, fully migrated schema.
func New(t *testing.T) *sql.DB {
	t.Helper()
	pool, _ := NewWithURL(t)
	return pool
}

// NewWithURL is New plus the connection URL of the schema, for code that
// opens its own connections (e.g. a LISTEN connection).
func NewWithURL(t *testing.T) (*sql.DB, string) {
	t.Helper()
	pool, url := empty(t)
	if _, err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	return pool, url
}

// Empty returns a pool on a fresh schema with no tables.
func Empty(t *testing.T) *sql.DB {
	t.Helper()
	pool, _ := empty(t)
	return pool
}

func empty(t *testing.T) (*sql.DB, string) {
	t.Helper()
	raw := os.Getenv("PLURB_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("PLURB_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := db.Open(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	var name string
	if err := admin.QueryRowContext(ctx, `SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(name, "_test") {
		t.Fatalf("refusing to use database %q: name must end in _test", name)
	}

	b := make([]byte, 6)
	rand.Read(b)
	schema := "t_" + hex.EncodeToString(b)
	// Extensions are database-wide; keep pg_trgm in public, visible to all schemas.
	if _, err := admin.ExecContext(ctx,
		`CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public; CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	// lib/pq forwards unknown keys as session parameters.
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()

	pool, err := db.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if a, err := db.Open(context.Background(), raw); err == nil {
			a.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
			a.Close()
		}
	})
	return pool, u.String()
}
