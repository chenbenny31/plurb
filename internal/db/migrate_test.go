package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/chenbenny31/plurb/internal/db"
	"github.com/chenbenny31/plurb/internal/db/dbtest"
)

func TestMigrateIsIdempotent(t *testing.T) {
	pool := dbtest.New(t)
	again, err := db.Migrate(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("second run applied %v, want nothing", again)
	}
}

func TestSchemaRejectsNonHTTPSCover(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	var uid int64
	err := pool.QueryRowContext(ctx, `INSERT INTO users (username, display_name, password_hash)
		VALUES ('benny', 'Benny', 'x') RETURNING id`).Scan(&uid)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.ExecContext(ctx, `INSERT INTO posts (author_id, slug, title, cover_url)
		VALUES ($1, 'hello', 'Hello', 'http://example.com/a.png')`, uid)
	if err == nil {
		t.Fatal("plain http cover_url was accepted")
	}
}

func TestMigrateRefusesEditedMigration(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	// Pretend 0003 looked different when it was applied.
	if _, err := pool.ExecContext(ctx,
		`UPDATE schema_migrations SET checksum = 'deadbeef' WHERE version = 3`); err != nil {
		t.Fatal(err)
	}
	_, err := db.Migrate(ctx, pool)
	if err == nil || !strings.Contains(err.Error(), "edited after it was applied") {
		t.Fatalf("edited migration not caught: %v", err)
	}
	st, err := db.Status(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if !st[2].Edited || st[0].Edited {
		t.Fatalf("status = %+v", st)
	}
}

func TestOlderBinaryToleratesNewerSchema(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	// A rollback: the database has a migration this binary doesn't know.
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum) VALUES (9999, '9999_future.sql', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("older binary refused a newer schema: %v", err)
	}
	st, err := db.Status(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	last := st[len(st)-1]
	if !last.Unknown || last.Version != 9999 {
		t.Fatalf("status = %+v", st)
	}
}

func TestLegacyRowsGetChecksums(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	pool.ExecContext(ctx, `UPDATE schema_migrations SET checksum = ''`)
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var blank int
	pool.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE checksum = ''`).Scan(&blank)
	if blank != 0 {
		t.Fatalf("%d migrations still lack a checksum", blank)
	}
}
