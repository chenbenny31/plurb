package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Migration files are named NNNN_description.sql and applied in version order,
// each inside its own transaction. Applied files must never be edited — add a
// new file instead. The migrator enforces this: it records each file's SHA-256
// when applying it and refuses to start if an applied file has changed.
//
// Upgrades are expand/contract: a migration must keep working with the
// previous release's binary (add columns and tables; drop or rename only in a
// later release), so a deploy can roll back the binary without touching data.
// A binary that finds migrations newer than itself still starts.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// migrateLockID is an arbitrary key for pg_advisory_lock, so two processes
// starting at once don't race to apply the same migration.
const migrateLockID = 7_401_552

type migration struct {
	version  int
	name     string
	sql      string
	checksum string // hex SHA-256 of the file
}

func loadMigrations() ([]migration, error) {
	paths, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	var ms []migration
	seen := map[int]string{}
	for _, p := range paths {
		name := strings.TrimPrefix(p, "migrations/")
		num, _, ok := strings.Cut(name, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil {
			return nil, fmt.Errorf("migration %s: name must look like 0001_name.sql", name)
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("migrations %s and %s share version %d", prev, name, v)
		}
		seen[v] = name
		body, err := migrationFS.ReadFile(p)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		ms = append(ms, migration{version: v, name: name, sql: string(body), checksum: hex.EncodeToString(sum[:])})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	return ms, nil
}

// Migrate applies every embedded migration that hasn't run yet and returns
// the names of the ones it applied.
func Migrate(ctx context.Context, pool *sql.DB) ([]string, error) {
	ms, err := loadMigrations()
	if err != nil {
		return nil, err
	}

	// Advisory locks are per-connection, so pin one connection for the whole run.
	conn, err := pool.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrateLockID); err != nil {
		return nil, fmt.Errorf("acquire migration lock: %w", err)
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, migrateLockID)

	if err := ensureTable(ctx, conn); err != nil {
		return nil, err
	}
	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return nil, err
	}

	var done []string
	for _, m := range ms {
		if a, ok := applied[m.version]; ok {
			switch {
			case a.checksum == "":
				// Applied before checksums were recorded: trust the file as it is now.
				if _, err := conn.ExecContext(ctx,
					`UPDATE schema_migrations SET checksum = $1 WHERE version = $2`, m.checksum, m.version); err != nil {
					return done, err
				}
			case a.checksum != m.checksum:
				return done, fmt.Errorf("migration %s was edited after it was applied "+
					"(recorded sha256 %.12s…, file %.12s…); restore the file and add a new migration instead",
					m.name, a.checksum, m.checksum)
			}
			continue
		}
		if err := applyOne(ctx, conn, m); err != nil {
			return done, fmt.Errorf("migration %s: %w", m.name, err)
		}
		done = append(done, m.name)
	}
	return done, nil
}

type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func ensureTable(ctx context.Context, q queryer) error {
	if _, err := q.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INT PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	return nil
}

type appliedRow struct {
	name      string
	checksum  string
	appliedAt time.Time
}

func appliedVersions(ctx context.Context, q queryer) (map[int]appliedRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT version, name, checksum, applied_at FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	applied := map[int]appliedRow{}
	for rows.Next() {
		var v int
		var a appliedRow
		if err := rows.Scan(&v, &a.name, &a.checksum, &a.appliedAt); err != nil {
			return nil, err
		}
		applied[v] = a
	}
	return applied, rows.Err()
}

// MigrationStatus is one row of `plurb migrate -status`.
type MigrationStatus struct {
	Version   int
	Name      string
	AppliedAt time.Time // zero = pending
	Edited    bool      // applied, but the file has changed since
	Unknown   bool      // applied by a newer binary; this one doesn't have the file
}

// Status compares the embedded migrations with what the database has applied,
// without changing anything.
func Status(ctx context.Context, pool *sql.DB) ([]MigrationStatus, error) {
	ms, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	if err := ensureTable(ctx, pool); err != nil {
		return nil, err
	}
	applied, err := appliedVersions(ctx, pool)
	if err != nil {
		return nil, err
	}
	var out []MigrationStatus
	for _, m := range ms {
		st := MigrationStatus{Version: m.version, Name: m.name}
		if a, ok := applied[m.version]; ok {
			st.AppliedAt = a.appliedAt
			st.Edited = a.checksum != "" && a.checksum != m.checksum
			delete(applied, m.version)
		}
		out = append(out, st)
	}
	for v, a := range applied {
		out = append(out, MigrationStatus{Version: v, Name: a.name, AppliedAt: a.appliedAt, Unknown: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

func applyOne(ctx context.Context, conn *sql.Conn, m migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// No args: lib/pq uses the simple query protocol, so a file may hold
	// several statements.
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`,
		m.version, m.name, m.checksum); err != nil {
		return err
	}
	return tx.Commit()
}
