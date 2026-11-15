package legacy

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/chenbenny31/plurb/internal/db/dbtest"
	"github.com/chenbenny31/plurb/internal/store"
)

func TestImport(t *testing.T) {
	ctx := context.Background()
	old := dbtest.Empty(t)
	schema, err := os.ReadFile("testdata/legacy_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`
		INSERT INTO users (username, password_hash, display_name, about_md)
			VALUES ('chenbenny31', '$2a$10$x', 'Benny', 'hi **there**');
		INSERT INTO series (author_id, title, slug, description) VALUES (1, 'Rust Notes', 'rust-notes', 'notes');
		INSERT INTO posts (author_id, title, slug, body_md, status, published_at, description, cover_image_url, view_count) VALUES
			(1, 'Hello', 'hello', 'first', 'PUBLISHED', now() - interval '3 days', 'd', 'https://img.example/a.png', 42),
			(1, 'Weird', 'Weird_Slug!', 'second', 'PUBLISHED', NULL, NULL, 'http://insecure/a.png', 0),
			(1, 'Draft', 'draft', 'wip', 'DRAFT', NULL, NULL, NULL, 0);
		INSERT INTO post_series VALUES (1, 1, 1), (2, 1, 2);
		INSERT INTO tags (name, slug) VALUES ('rust', 'rust'), ('C++', 'c');
		INSERT INTO post_tags VALUES (1, 1), (1, 2), (2, 1);`)
	if err != nil {
		t.Fatal(err)
	}

	dst := dbtest.New(t)
	rep, err := Import(ctx, old, dst)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Users != 1 || rep.Posts != 3 || rep.Series != 1 || rep.Tags != 2 {
		t.Fatalf("report: %+v", rep)
	}

	st := store.New(dst)
	u, err := st.UserByUsername(ctx, "chenbenny31")
	if err != nil || u.PasswordHash != "" || u.Role != "admin" || u.AboutMD != "hi **there**" {
		t.Fatalf("user: %+v %v", u, err)
	}
	p, err := st.PostBySlug(ctx, u.ID, "hello")
	if err != nil || !p.Published() || p.ViewCount != 42 || p.CoverURL == "" || len(p.Tags) != 2 || !p.SeriesID.Valid {
		t.Fatalf("hello: %+v %v", p, err)
	}
	weird, err := st.PostBySlug(ctx, u.ID, "weird-slug")
	if err != nil || weird.PublishedAt == nil || weird.CoverURL != "" {
		t.Fatalf("weird slug post: %+v %v", weird, err)
	}
	if !strings.Contains(strings.Join(rep.Warnings, "\n"), `slug changed to "weird-slug"`) {
		t.Errorf("warnings: %v", rep.Warnings)
	}
	cpp, err := st.TagBySlug(ctx, "cpp")
	if err != nil || cpp.Name != "C++" {
		t.Fatalf("c++ tag: %+v %v", cpp, err)
	}

	if _, err := Import(ctx, old, dst); err == nil {
		t.Fatal("second import into a non-empty database should fail")
	}
}
