package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode"
)

// PostInput is everything the editor form sets on a post.
type PostInput struct {
	Title             string
	Slug              string // empty = derive from title
	Description       string
	CoverURL          string
	BodyMD            string
	Tags              []string // display names; slugs are derived
	SeriesTitle       string   // empty = not in a series; new titles create a series
	SeriesDescription string
}

// Slug turns a title into a URL slug. Titles with no ASCII letters or digits
// (e.g. all-Chinese) get a short stable hash instead of an empty slug.
func Slug(title, prefix string, maxLen int) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	s := b.String()
	if len(s) > maxLen {
		s = strings.TrimRight(s[:maxLen], "-")
	}
	if s == "" {
		sum := sha256.Sum256([]byte(title))
		s = prefix + "-" + hex.EncodeToString(sum[:3])
	}
	return s
}

// SlugTaken reports whether the author already has another post with slug.
func (s *Store) SlugTaken(ctx context.Context, authorID int64, slug string, excludeID int64) (bool, error) {
	var taken bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM posts WHERE author_id = $1 AND slug = $2 AND id <> $3)`,
		authorID, slug, excludeID).Scan(&taken)
	return taken, err
}

// FreeSlug appends -2, -3, ... until the slug is unused by the author.
func (s *Store) FreeSlug(ctx context.Context, authorID int64, base string, excludeID int64) (string, error) {
	slug := base
	for i := 2; ; i++ {
		taken, err := s.SlugTaken(ctx, authorID, slug, excludeID)
		if err != nil || !taken {
			return slug, err
		}
		slug = base + "-" + strconv.Itoa(i)
	}
}

// CreatePost inserts a draft owned by authorID.
func (s *Store) CreatePost(ctx context.Context, authorID int64, in PostInput) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO posts (author_id, slug, title, description, cover_url, body_md)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		authorID, in.Slug, in.Title, in.Description, in.CoverURL, in.BodyMD).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrConflict
		}
		return 0, err
	}
	if err := setTags(ctx, tx, id, in.Tags); err != nil {
		return 0, err
	}
	if err := setSeries(ctx, tx, authorID, id, in.SeriesTitle, in.SeriesDescription); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// UpdatePost rewrites a post's content. Returns ErrNotFound unless the post
// exists and belongs to authorID.
func (s *Store) UpdatePost(ctx context.Context, authorID, postID int64, in PostInput) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		UPDATE posts SET slug = $3, title = $4, description = $5, cover_url = $6, body_md = $7,
			updated_at = now()
		WHERE id = $1 AND author_id = $2`,
		postID, authorID, in.Slug, in.Title, in.Description, in.CoverURL, in.BodyMD)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := setTags(ctx, tx, postID, in.Tags); err != nil {
		return err
	}
	if err := setSeries(ctx, tx, authorID, postID, in.SeriesTitle, in.SeriesDescription); err != nil {
		return err
	}
	return tx.Commit()
}

// SetPublished publishes or unpublishes. The first publish date is kept
// across unpublish/republish so permalinks and feeds stay stable.
func (s *Store) SetPublished(ctx context.Context, authorID, postID int64, publish bool) error {
	status := StatusDraft
	if publish {
		status = StatusPublished
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE posts SET status = $3,
			published_at = CASE WHEN $3 = 'published' THEN COALESCE(published_at, now()) ELSE published_at END,
			updated_at = now()
		WHERE id = $1 AND author_id = $2`, postID, authorID, status)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeletePost(ctx context.Context, authorID, postID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM posts WHERE id = $1 AND author_id = $2`, postID, authorID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// tagSlugReplacer keeps language tags apart: "c++" -> "cpp", "c#" -> "csharp".
var tagSlugReplacer = strings.NewReplacer("++", "pp", "#", "sharp")

func setTags(ctx context.Context, tx *sql.Tx, postID int64, names []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM post_tags WHERE post_id = $1`, postID); err != nil {
		return err
	}
	for _, name := range names {
		slug := Slug(tagSlugReplacer.Replace(name), "t", 64)
		// The first spelling of a tag wins ("Go" and "go" share slug "go").
		var tagID int64
		err := tx.QueryRowContext(ctx, `
			WITH ins AS (
				INSERT INTO tags (slug, name) VALUES ($1, $2)
				ON CONFLICT (slug) DO NOTHING RETURNING id
			)
			SELECT id FROM ins UNION ALL SELECT id FROM tags WHERE slug = $1 LIMIT 1`,
			slug, name).Scan(&tagID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO post_tags (post_id, tag_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			postID, tagID); err != nil {
			return err
		}
	}
	return nil
}

// setSeries finds or creates the author's series by title and places the
// post at its end. An empty title removes the post from any series.
func setSeries(ctx context.Context, tx *sql.Tx, authorID, postID int64, title, description string) error {
	if title == "" {
		_, err := tx.ExecContext(ctx, `UPDATE posts SET series_id = NULL, series_pos = NULL WHERE id = $1`, postID)
		return err
	}
	slug := Slug(title, "series", 120)
	var seriesID int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO series (author_id, slug, title, description) VALUES ($1, $2, $3, $4)
		ON CONFLICT (author_id, slug) DO UPDATE
			SET description = CASE WHEN EXCLUDED.description <> '' THEN EXCLUDED.description
			                       ELSE series.description END
		RETURNING id`, authorID, slug, title, description).Scan(&seriesID)
	if err != nil {
		return err
	}
	// Only move the post to the end if it's joining a different series.
	_, err = tx.ExecContext(ctx, `
		UPDATE posts SET series_id = $2,
			series_pos = (SELECT COALESCE(max(series_pos), 0) + 1 FROM posts WHERE series_id = $2)
		WHERE id = $1 AND series_id IS DISTINCT FROM $2`, postID, seriesID)
	return err
}

// AuthorTags lists tags the author has used, for the editor's quick-pick chips.
func (s *Store) AuthorTags(ctx context.Context, authorID int64) ([]Tag, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.slug, t.name, count(*)
		FROM tags t JOIN post_tags pt ON pt.tag_id = t.id JOIN posts p ON p.id = pt.post_id
		WHERE p.author_id = $1
		GROUP BY t.id ORDER BY count(*) DESC, t.name LIMIT 30`, authorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Slug, &t.Name, &t.Count); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

// SeriesByAuthor lists all of an author's series, drafts-only ones included.
func (s *Store) SeriesByAuthor(ctx context.Context, authorID int64) ([]Series, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT se.id, se.author_id, se.slug, se.title, se.description, se.created_at, u.username,
			(SELECT count(*) FROM posts p WHERE p.series_id = se.id)
		FROM series se JOIN users u ON u.id = se.author_id
		WHERE se.author_id = $1 ORDER BY se.title`, authorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Series
	for rows.Next() {
		var se Series
		if err := rows.Scan(&se.ID, &se.AuthorID, &se.Slug, &se.Title, &se.Description,
			&se.CreatedAt, &se.AuthorUsername, &se.PostCount); err != nil {
			return nil, err
		}
		out = append(out, se)
	}
	return out, rows.Err()
}
