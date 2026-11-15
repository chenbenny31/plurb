package store

import (
	"context"
	"time"
)

type Series struct {
	ID          int64
	AuthorID    int64
	Slug        string
	Title       string
	Description string
	CreatedAt   time.Time

	AuthorUsername string
	PostCount      int // published posts in the series
}

// SeriesWithPosts lists series that contain at least one published post,
// optionally limited to one author (authorID 0 = everyone).
func (s *Store) SeriesWithPosts(ctx context.Context, authorID int64) ([]Series, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT se.id, se.author_id, se.slug, se.title, se.description, se.created_at, u.username, count(p.id)
		FROM series se
		JOIN users u ON u.id = se.author_id
		JOIN posts p ON p.series_id = se.id AND p.status = 'published'
		WHERE $1 = 0 OR se.author_id = $1
		GROUP BY se.id, u.username
		ORDER BY max(p.published_at) DESC`, authorID)
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

func (s *Store) SeriesBySlug(ctx context.Context, authorID int64, slug string) (*Series, error) {
	se := &Series{}
	err := s.db.QueryRowContext(ctx, `
		SELECT se.id, se.author_id, se.slug, se.title, se.description, se.created_at, u.username
		FROM series se JOIN users u ON u.id = se.author_id
		WHERE se.author_id = $1 AND se.slug = $2`, authorID, slug).
		Scan(&se.ID, &se.AuthorID, &se.Slug, &se.Title, &se.Description, &se.CreatedAt, &se.AuthorUsername)
	if err != nil {
		return nil, notFound(err)
	}
	return se, nil
}

func (s *Store) SeriesByID(ctx context.Context, id int64) (*Series, error) {
	se := &Series{}
	err := s.db.QueryRowContext(ctx, `
		SELECT se.id, se.author_id, se.slug, se.title, se.description, se.created_at, u.username
		FROM series se JOIN users u ON u.id = se.author_id
		WHERE se.id = $1`, id).
		Scan(&se.ID, &se.AuthorID, &se.Slug, &se.Title, &se.Description, &se.CreatedAt, &se.AuthorUsername)
	if err != nil {
		return nil, notFound(err)
	}
	return se, nil
}

// SeriesPosts lists a series' published posts in reading order.
func (s *Store) SeriesPosts(ctx context.Context, seriesID int64) ([]*Post, error) {
	return s.queryPosts(ctx, `SELECT `+postCols+postFrom+`
		WHERE p.series_id = $1 AND p.status = 'published'
		ORDER BY p.series_pos NULLS LAST, p.published_at`, seriesID)
}
