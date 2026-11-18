package store

import (
	"context"
	"fmt"
)

// UserStats is the public activity and reputation summary on an author page.
type UserStats struct {
	Posts, Threads, Comments int // published posts, threads started, comments written

	UpvotesReceived, DownvotesReceived int // live votes on their threads and comments

	FromThreads, FromComments int // net reputation by where it was earned
	Last30Days                int // net reputation change in the last 30 days
}

// UserStats gathers an author's public stats in one round trip: counts from
// the content tables, live votes from votes, and net points from the
// append-only rep_events ledger (so retracted votes are already netted out).
func (s *Store) UserStats(ctx context.Context, userID int64) (*UserStats, error) {
	var st UserStats
	err := s.db.QueryRowContext(ctx, `
		SELECT
		  (SELECT count(*) FROM posts    WHERE author_id = $1 AND status = 'published'),
		  (SELECT count(*) FROM threads  WHERE author_id = $1),
		  (SELECT count(*) FROM comments WHERE author_id = $1),
		  (SELECT count(*) FILTER (WHERE v.value > 0) FROM votes v
		     LEFT JOIN threads  t ON v.target_type = 'thread'  AND t.id = v.target_id
		     LEFT JOIN comments c ON v.target_type = 'comment' AND c.id = v.target_id
		   WHERE COALESCE(t.author_id, c.author_id) = $1),
		  (SELECT count(*) FILTER (WHERE v.value < 0) FROM votes v
		     LEFT JOIN threads  t ON v.target_type = 'thread'  AND t.id = v.target_id
		     LEFT JOIN comments c ON v.target_type = 'comment' AND c.id = v.target_id
		   WHERE COALESCE(t.author_id, c.author_id) = $1),
		  (SELECT COALESCE(sum(delta), 0) FROM rep_events WHERE user_id = $1 AND target_type = 'thread'),
		  (SELECT COALESCE(sum(delta), 0) FROM rep_events WHERE user_id = $1 AND target_type = 'comment'),
		  (SELECT COALESCE(sum(delta), 0) FROM rep_events WHERE user_id = $1 AND created_at > now() - interval '30 days')`,
		userID).Scan(&st.Posts, &st.Threads, &st.Comments,
		&st.UpvotesReceived, &st.DownvotesReceived,
		&st.FromThreads, &st.FromComments, &st.Last30Days)
	if err != nil {
		return nil, fmt.Errorf("user stats: %w", err)
	}
	return &st, nil
}

// ThreadsByAuthor lists the threads a user started, newest first.
func (s *Store) ThreadsByAuthor(ctx context.Context, authorID int64, limit int) ([]*Thread, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+threadCols+`
		FROM threads t JOIN users u ON u.id = t.author_id
		WHERE t.author_id = $1 ORDER BY t.created_at DESC, t.id DESC LIMIT $2`, authorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Thread
	for rows.Next() {
		t, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
