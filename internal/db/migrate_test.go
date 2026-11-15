package db_test

import (
	"context"
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
