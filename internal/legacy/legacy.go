// Package legacy imports data from the old Java/Spring plurb database.
//
// The old schema (Flyway V1–V8) used upper-case statuses, a post_series
// join table and bcrypt password hashes. Imported accounts get no password:
// bcrypt hashes can't be verified here, so owners run `plurb passwd` (or
// link GitHub) after the import.
package legacy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chenbenny31/plurb/internal/store"
)

type Report struct {
	Users, Posts, Tags, Series int
	Warnings                   []string
}

func (r *Report) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// Import copies users, series, posts and tags from old into the new
// database inside one transaction. It refuses to run if the new database
// already has users, so it can't be applied twice by accident.
func Import(ctx context.Context, old, dst *sql.DB) (*Report, error) {
	var n int
	if err := dst.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, errors.New("target database already has users; import only into a fresh database")
	}

	tx, err := dst.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rep := &Report{}

	users, err := importUsers(ctx, old, tx, rep)
	if err != nil {
		return nil, fmt.Errorf("users: %w", err)
	}
	series, err := importSeries(ctx, old, tx, users, rep)
	if err != nil {
		return nil, fmt.Errorf("series: %w", err)
	}
	posts, err := importPosts(ctx, old, tx, users, series, rep)
	if err != nil {
		return nil, fmt.Errorf("posts: %w", err)
	}
	if err := importTags(ctx, old, tx, posts, rep); err != nil {
		return nil, fmt.Errorf("tags: %w", err)
	}
	return rep, tx.Commit()
}

// importUsers returns old user id -> new user id.
func importUsers(ctx context.Context, old *sql.DB, tx *sql.Tx, rep *Report) (map[int64]int64, error) {
	rows, err := old.QueryContext(ctx, `
		SELECT id, username, COALESCE(display_name, ''), COALESCE(about_md, ''), created_at
		FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[int64]int64{}
	for rows.Next() {
		var oldID int64
		var username, display, about string
		var created time.Time
		if err := rows.Scan(&oldID, &username, &display, &about, &created); err != nil {
			return nil, err
		}
		name := strings.ToLower(username)
		if !store.ValidUsername(name) {
			rep.warn("skipped user %q: username not valid in the new schema", username)
			continue
		}
		if display == "" {
			display = name
		}
		display = truncate(display, 64)
		var newID int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO users (username, display_name, password_hash, about_md, role, created_at)
			VALUES ($1, $2, '', $3, CASE WHEN $4 THEN 'admin' ELSE 'member' END, $5)
			RETURNING id`, name, display, about, len(ids) == 0, created).Scan(&newID)
		if err != nil {
			return nil, err
		}
		ids[oldID] = newID
		rep.Users++
	}
	return ids, rows.Err()
}

// importSeries returns old series id -> new series id.
func importSeries(ctx context.Context, old *sql.DB, tx *sql.Tx, users map[int64]int64, rep *Report) (map[int64]int64, error) {
	rows, err := old.QueryContext(ctx, `
		SELECT id, author_id, slug, title, COALESCE(description, ''), created_at FROM series ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[int64]int64{}
	for rows.Next() {
		var oldID, author int64
		var slug, title, desc string
		var created time.Time
		if err := rows.Scan(&oldID, &author, &slug, &title, &desc, &created); err != nil {
			return nil, err
		}
		newAuthor, ok := users[author]
		if !ok {
			continue
		}
		var newID int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO series (author_id, slug, title, description, created_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (author_id, slug) DO UPDATE SET title = series.title
			RETURNING id`,
			newAuthor, store.Slug(slug, "series", 120), truncate(title, 200), truncate(desc, 300), created).Scan(&newID)
		if err != nil {
			return nil, err
		}
		ids[oldID] = newID
		rep.Series++
	}
	return ids, rows.Err()
}

// importPosts returns old post id -> new post id.
func importPosts(ctx context.Context, old *sql.DB, tx *sql.Tx, users, series map[int64]int64, rep *Report) (map[int64]int64, error) {
	rows, err := old.QueryContext(ctx, `
		SELECT p.id, p.author_id, p.slug, p.title, COALESCE(p.description, ''),
			COALESCE(p.cover_image_url, ''), p.body_md, p.status, p.published_at,
			p.created_at, p.updated_at, p.view_count,
			ps.series_id, ps.position
		FROM posts p
		LEFT JOIN LATERAL (
			SELECT series_id, position FROM post_series WHERE post_id = p.id ORDER BY series_id LIMIT 1
		) ps ON true
		ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[int64]int64{}
	// author id -> slugs used, to keep slugs unique after sanitising
	used := map[int64]map[string]bool{}
	for rows.Next() {
		var oldID, author, views int64
		var slug, title, desc, cover, body, status string
		var published sql.NullTime
		var created, updated time.Time
		var oldSeries sql.NullInt64
		var pos sql.NullInt32
		if err := rows.Scan(&oldID, &author, &slug, &title, &desc, &cover, &body, &status,
			&published, &created, &updated, &views, &oldSeries, &pos); err != nil {
			return nil, err
		}
		newAuthor, ok := users[author]
		if !ok {
			continue
		}
		if len(body) > 200_000 {
			rep.warn("skipped post %q: body over 200 KB", slug)
			continue
		}
		if cover != "" && !strings.HasPrefix(cover, "https://") {
			rep.warn("post %q: dropped non-https cover %q", slug, cover)
			cover = ""
		}
		newSlug := uniqueSlug(used, newAuthor, store.Slug(slug, "post", 120))
		if newSlug != slug {
			rep.warn("post %q: slug changed to %q", slug, newSlug)
		}
		status = strings.ToLower(status)
		if status != store.StatusPublished {
			status = store.StatusDraft
		}
		if status == store.StatusPublished && !published.Valid {
			published = sql.NullTime{Time: created, Valid: true}
		}
		var seriesID sql.NullInt64
		if oldSeries.Valid {
			if id, ok := series[oldSeries.Int64]; ok {
				seriesID = sql.NullInt64{Int64: id, Valid: true}
			}
		}
		if title == "" {
			title = newSlug
		}

		var newID int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO posts (author_id, slug, title, description, cover_url, body_md, status,
				series_id, series_pos, view_count, published_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13) RETURNING id`,
			newAuthor, newSlug, truncate(title, 200), truncate(desc, 300), cover, body, status,
			seriesID, nullPos(seriesID, pos), views, published, created, updated).Scan(&newID)
		if err != nil {
			return nil, fmt.Errorf("post %q: %w", slug, err)
		}
		ids[oldID] = newID
		rep.Posts++
	}
	return ids, rows.Err()
}

func importTags(ctx context.Context, old *sql.DB, tx *sql.Tx, posts map[int64]int64, rep *Report) error {
	rows, err := old.QueryContext(ctx, `
		SELECT pt.post_id, t.name FROM post_tags pt JOIN tags t ON t.id = pt.tag_id ORDER BY t.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var oldPost int64
		var name string
		if err := rows.Scan(&oldPost, &name); err != nil {
			return err
		}
		postID, ok := posts[oldPost]
		if !ok {
			continue
		}
		name = truncate(name, 64)
		slug := store.Slug(strings.NewReplacer("++", "pp", "#", "sharp").Replace(name), "t", 64)
		if _, err := tx.ExecContext(ctx, `
			WITH ins AS (
				INSERT INTO tags (slug, name) VALUES ($1, $2) ON CONFLICT (slug) DO NOTHING RETURNING id
			), tag AS (
				SELECT id FROM ins UNION ALL SELECT id FROM tags WHERE slug = $1 LIMIT 1
			)
			INSERT INTO post_tags (post_id, tag_id) SELECT $3, id FROM tag ON CONFLICT DO NOTHING`,
			slug, name, postID); err != nil {
			return err
		}
		if !seen[slug] {
			seen[slug] = true
			rep.Tags++
		}
	}
	return rows.Err()
}

func uniqueSlug(used map[int64]map[string]bool, author int64, base string) string {
	if used[author] == nil {
		used[author] = map[string]bool{}
	}
	slug := base
	for i := 2; used[author][slug]; i++ {
		slug = fmt.Sprintf("%s-%d", base, i)
	}
	used[author][slug] = true
	return slug
}

func nullPos(series sql.NullInt64, pos sql.NullInt32) sql.NullInt32 {
	if !series.Valid {
		return sql.NullInt32{}
	}
	return pos
}

func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}
