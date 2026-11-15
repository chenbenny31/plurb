package store

import (
	"context"
	"database/sql"
	"strconv"
	"time"
)

// DefaultNotifyChannel is the Postgres NOTIFY channel for new notifications.
// The payload is the recipient's user id.
const DefaultNotifyChannel = "plurb_notify"

type Notification struct {
	ID        int64
	Kind      string // "thread_reply" or "comment_reply"
	ThreadID  int64
	CommentID int64
	Read      bool
	CreatedAt time.Time

	ActorDisplayName string
	ThreadTitle      string
}

// notifyReply records a notification for whoever the new comment answers
// and signals listeners. It runs inside the comment's transaction, and
// Postgres only delivers NOTIFY on commit, so listeners never see a
// notification for a comment that rolled back.
func (s *Store) notifyReply(ctx context.Context, tx *sql.Tx, threadID, parentID, commentID, actorID int64) error {
	var recipient int64
	kind := "thread_reply"
	var err error
	if parentID > 0 {
		kind = "comment_reply"
		err = tx.QueryRowContext(ctx, `SELECT author_id FROM comments WHERE id = $1`, parentID).Scan(&recipient)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT author_id FROM threads WHERE id = $1`, threadID).Scan(&recipient)
	}
	if err != nil {
		return err
	}
	if recipient == actorID {
		return nil // no notifications for replying to yourself
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO notifications (user_id, kind, actor_id, thread_id, comment_id)
		VALUES ($1, $2, $3, $4, $5)`, recipient, kind, actorID, threadID, commentID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `SELECT pg_notify($1, $2)`, s.NotifyChannel(), strconv.FormatInt(recipient, 10))
	return err
}

// NotifyChannel returns the channel this store notifies on.
func (s *Store) NotifyChannel() string {
	if s.notifyChannel != "" {
		return s.notifyChannel
	}
	return DefaultNotifyChannel
}

// SetNotifyChannel overrides the channel (tests use a unique one each).
func (s *Store) SetNotifyChannel(name string) { s.notifyChannel = name }

func (s *Store) UnreadCount(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM notifications WHERE user_id = $1 AND read_at IS NULL`, userID).Scan(&n)
	return n, err
}

func (s *Store) Notifications(ctx context.Context, userID int64, limit int) ([]Notification, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.id, n.kind, n.thread_id, n.comment_id, n.read_at IS NOT NULL, n.created_at,
			COALESCE(u.display_name, 'someone'), t.title
		FROM notifications n
		JOIN threads t ON t.id = n.thread_id
		LEFT JOIN users u ON u.id = n.actor_id
		WHERE n.user_id = $1 ORDER BY n.id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.Kind, &n.ThreadID, &n.CommentID, &n.Read, &n.CreatedAt,
			&n.ActorDisplayName, &n.ThreadTitle); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) MarkNotificationsRead(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE notifications SET read_at = now() WHERE user_id = $1 AND read_at IS NULL`, userID)
	return err
}
