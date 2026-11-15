package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
)

const (
	StatusDraft     = "draft"
	StatusPublished = "published"
)

type Post struct {
	ID          int64
	AuthorID    int64
	Slug        string
	Title       string
	Description string
	CoverURL    string
	BodyMD      string
	Status      string
	SeriesID    sql.NullInt64
	SeriesPos   sql.NullInt32
	ViewCount   int64
	PublishedAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// Joined for display.
	AuthorUsername    string
	AuthorDisplayName string
	Tags              []Tag
}

func (p *Post) Published() bool { return p.Status == StatusPublished }

type Tag struct {
	ID    int64
	Slug  string
	Name  string
	Count int // published posts using the tag, when queried with counts
}

const postCols = `p.id, p.author_id, p.slug, p.title, p.description, p.cover_url, p.body_md,
	p.status, p.series_id, p.series_pos, p.view_count, p.published_at, p.created_at, p.updated_at,
	u.username, u.display_name`

const postFrom = ` FROM posts p JOIN users u ON u.id = p.author_id `

func scanPost(row interface{ Scan(...any) error }) (*Post, error) {
	p := &Post{}
	err := row.Scan(&p.ID, &p.AuthorID, &p.Slug, &p.Title, &p.Description, &p.CoverURL, &p.BodyMD,
		&p.Status, &p.SeriesID, &p.SeriesPos, &p.ViewCount, &p.PublishedAt, &p.CreatedAt, &p.UpdatedAt,
		&p.AuthorUsername, &p.AuthorDisplayName)
	if err != nil {
		return nil, notFound(err)
	}
	return p, nil
}

// queryPosts runs a post query and attaches each post's tags.
func (s *Store) queryPosts(ctx context.Context, query string, args ...any) ([]*Post, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var posts []*Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		posts = append(posts, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return posts, s.attachTags(ctx, posts)
}

// attachTags loads tags for all posts in one query.
func (s *Store) attachTags(ctx context.Context, posts []*Post) error {
	if len(posts) == 0 {
		return nil
	}
	// post id -> post
	byID := make(map[int64]*Post, len(posts))
	ids := make([]int64, len(posts))
	for i, p := range posts {
		byID[p.ID] = p
		ids[i] = p.ID
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT pt.post_id, t.id, t.slug, t.name
		FROM post_tags pt JOIN tags t ON t.id = pt.tag_id
		WHERE pt.post_id = ANY($1)
		ORDER BY t.name`, pq.Array(ids))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var pid int64
		var t Tag
		if err := rows.Scan(&pid, &t.ID, &t.Slug, &t.Name); err != nil {
			return err
		}
		byID[pid].Tags = append(byID[pid].Tags, t)
	}
	return rows.Err()
}

// ListPublished pages through all published posts, newest first, optionally
// filtered by tag slug. It also returns the total for pagination.
func (s *Store) ListPublished(ctx context.Context, tagSlug string, limit, offset int) ([]*Post, int, error) {
	where := ` WHERE p.status = 'published'`
	args := []any{}
	if tagSlug != "" {
		where += ` AND EXISTS (SELECT 1 FROM post_tags pt JOIN tags t ON t.id = pt.tag_id
			WHERE pt.post_id = p.id AND t.slug = $1)`
		args = append(args, tagSlug)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*)`+postFrom+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	n := len(args)
	posts, err := s.queryPosts(ctx, `SELECT `+postCols+postFrom+where+
		` ORDER BY p.published_at DESC LIMIT $`+strconv.Itoa(n+1)+` OFFSET $`+strconv.Itoa(n+2),
		append(args, limit, offset)...)
	return posts, total, err
}

// PublishedByAuthor lists an author's published posts, optionally by tag.
func (s *Store) PublishedByAuthor(ctx context.Context, authorID int64, tagSlug string) ([]*Post, error) {
	q := `SELECT ` + postCols + postFrom + ` WHERE p.author_id = $1 AND p.status = 'published'`
	args := []any{authorID}
	if tagSlug != "" {
		q += ` AND EXISTS (SELECT 1 FROM post_tags pt JOIN tags t ON t.id = pt.tag_id
			WHERE pt.post_id = p.id AND t.slug = $2)`
		args = append(args, tagSlug)
	}
	return s.queryPosts(ctx, q+` ORDER BY p.published_at DESC`, args...)
}

// PostsByAuthor lists every post (drafts included) for the author's dashboard.
func (s *Store) PostsByAuthor(ctx context.Context, authorID int64) ([]*Post, error) {
	return s.queryPosts(ctx, `SELECT `+postCols+postFrom+
		` WHERE p.author_id = $1 ORDER BY p.updated_at DESC`, authorID)
}

// PostBySlug returns a post in any status; callers decide who may see drafts.
func (s *Store) PostBySlug(ctx context.Context, authorID int64, slug string) (*Post, error) {
	posts, err := s.queryPosts(ctx, `SELECT `+postCols+postFrom+
		` WHERE p.author_id = $1 AND p.slug = $2`, authorID, slug)
	if err != nil {
		return nil, err
	}
	if len(posts) == 0 {
		return nil, ErrNotFound
	}
	return posts[0], nil
}

func (s *Store) PostByID(ctx context.Context, id int64) (*Post, error) {
	posts, err := s.queryPosts(ctx, `SELECT `+postCols+postFrom+` WHERE p.id = $1`, id)
	if err != nil {
		return nil, err
	}
	if len(posts) == 0 {
		return nil, ErrNotFound
	}
	return posts[0], nil
}

// Neighbours returns the author's published posts just before (older) and
// after (newer) p; either may be nil.
func (s *Store) Neighbours(ctx context.Context, p *Post) (older, newer *Post, err error) {
	if p.PublishedAt == nil {
		return nil, nil, nil
	}
	olderList, err := s.queryPosts(ctx, `SELECT `+postCols+postFrom+`
		WHERE p.author_id = $1 AND p.status = 'published' AND (p.published_at, p.id) < ($2, $3)
		ORDER BY p.published_at DESC, p.id DESC LIMIT 1`, p.AuthorID, *p.PublishedAt, p.ID)
	if err != nil {
		return nil, nil, err
	}
	newerList, err := s.queryPosts(ctx, `SELECT `+postCols+postFrom+`
		WHERE p.author_id = $1 AND p.status = 'published' AND (p.published_at, p.id) > ($2, $3)
		ORDER BY p.published_at ASC, p.id ASC LIMIT 1`, p.AuthorID, *p.PublishedAt, p.ID)
	if err != nil {
		return nil, nil, err
	}
	if len(olderList) > 0 {
		older = olderList[0]
	}
	if len(newerList) > 0 {
		newer = newerList[0]
	}
	return older, newer, nil
}

// Related finds the author's other published posts sharing the most tags.
func (s *Store) Related(ctx context.Context, p *Post, limit int) ([]*Post, error) {
	return s.queryPosts(ctx, `SELECT `+postCols+postFrom+`
		JOIN (
			SELECT pt2.post_id, count(*) AS shared
			FROM post_tags pt1 JOIN post_tags pt2 ON pt2.tag_id = pt1.tag_id
			WHERE pt1.post_id = $1 AND pt2.post_id <> $1
			GROUP BY pt2.post_id
		) r ON r.post_id = p.id
		WHERE p.author_id = $2 AND p.status = 'published'
		ORDER BY r.shared DESC, p.published_at DESC
		LIMIT $3`, p.ID, p.AuthorID, limit)
}

func (s *Store) IncrementViews(ctx context.Context, postID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE posts SET view_count = view_count + 1 WHERE id = $1`, postID)
	return err
}

// Search matches published posts by english full-text search, falling back
// to substring match (trigram-indexed) so CJK and partial words still hit.
func (s *Store) Search(ctx context.Context, q string, limit int) ([]*Post, error) {
	like := "%" + escapeLike(q) + "%"
	return s.queryPosts(ctx, `SELECT `+postCols+postFrom+`
		WHERE p.status = 'published' AND (
			p.search_doc @@ websearch_to_tsquery('english', $1)
			OR (p.title || ' ' || p.description || ' ' || p.body_md) ILIKE $2
		)
		ORDER BY ts_rank(p.search_doc, websearch_to_tsquery('english', $1)) DESC, p.published_at DESC
		LIMIT $3`, q, like, limit)
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// TagsInUse lists tags on published posts with their counts, most used first.
func (s *Store) TagsInUse(ctx context.Context, limit int) ([]Tag, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.slug, t.name, count(*)
		FROM tags t
		JOIN post_tags pt ON pt.tag_id = t.id
		JOIN posts p ON p.id = pt.post_id AND p.status = 'published'
		GROUP BY t.id ORDER BY count(*) DESC, t.name LIMIT $1`, limit)
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

func (s *Store) TagBySlug(ctx context.Context, slug string) (*Tag, error) {
	t := &Tag{}
	err := s.db.QueryRowContext(ctx, `SELECT id, slug, name FROM tags WHERE slug = $1`, slug).
		Scan(&t.ID, &t.Slug, &t.Name)
	if err != nil {
		return nil, notFound(err)
	}
	return t, nil
}
