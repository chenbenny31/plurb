package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/chenbenny31/plurb/internal/rep"
)

var (
	ErrSelfVote       = errors.New("you can't vote on your own post")
	ErrDownvoteLocked = fmt.Errorf("downvoting needs %d reputation", rep.DownvoteMinRep)
	ErrCommentTooDeep = errors.New("reply nesting is too deep")
	ErrWrongThread    = errors.New("parent comment is in another thread")
	ErrBadVoteTarget  = errors.New("unknown vote target")
)

type Thread struct {
	ID             int64
	AuthorID       int64
	Title          string
	BodyMD         string
	Score          float64
	CommentCount   int
	CreatedAt      time.Time
	LastActivityAt time.Time

	AuthorUsername    string
	AuthorDisplayName string
	AuthorReputation  int
}

type Comment struct {
	ID        int64
	ThreadID  int64
	ParentID  sql.NullInt64
	AuthorID  int64
	BodyMD    string
	Score     float64
	Depth     int
	CreatedAt time.Time

	AuthorUsername    string
	AuthorDisplayName string
	AuthorReputation  int
}

// MaxCommentDepth matches the CHECK on comments.depth.
const MaxCommentDepth = 8

const threadCols = `t.id, t.author_id, t.title, t.body_md, t.score, t.comment_count, t.created_at,
	t.last_activity_at, u.username, u.display_name, u.reputation`

func scanThread(row interface{ Scan(...any) error }) (*Thread, error) {
	t := &Thread{}
	err := row.Scan(&t.ID, &t.AuthorID, &t.Title, &t.BodyMD, &t.Score, &t.CommentCount, &t.CreatedAt,
		&t.LastActivityAt, &t.AuthorUsername, &t.AuthorDisplayName, &t.AuthorReputation)
	if err != nil {
		return nil, notFound(err)
	}
	return t, nil
}

func (s *Store) CreateThread(ctx context.Context, authorID int64, title, body string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO threads (author_id, title, body_md) VALUES ($1, $2, $3) RETURNING id`,
		authorID, title, body).Scan(&id)
	return id, err
}

func (s *Store) ThreadByID(ctx context.Context, id int64) (*Thread, error) {
	return scanThread(s.db.QueryRowContext(ctx, `SELECT `+threadCols+`
		FROM threads t JOIN users u ON u.id = t.author_id WHERE t.id = $1`, id))
}

// Thread orderings. "hot" divides the weighted score by a power of age
// (see rep.Hot); the SQL expression mirrors it.
var threadOrder = map[string]string{
	"hot": fmt.Sprintf(`t.score / power(extract(epoch FROM now() - t.created_at) / 3600 + 2, %v) DESC, t.id DESC`, rep.Gravity),
	"new": `t.created_at DESC, t.id DESC`,
	"top": `t.score DESC, t.id DESC`,
}

// ListThreads pages through threads in the given order ("hot", "new", "top").
func (s *Store) ListThreads(ctx context.Context, sort string, limit, offset int) ([]*Thread, error) {
	order, ok := threadOrder[sort]
	if !ok {
		order = threadOrder["hot"]
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+threadCols+`
		FROM threads t JOIN users u ON u.id = t.author_id
		ORDER BY `+order+` LIMIT $1 OFFSET $2`, limit, offset)
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

func (s *Store) SearchThreads(ctx context.Context, q string, limit int) ([]*Thread, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+threadCols+`
		FROM threads t JOIN users u ON u.id = t.author_id
		WHERE t.search_doc @@ websearch_to_tsquery('english', $1)
		   OR (t.title || ' ' || t.body_md) ILIKE $2
		ORDER BY ts_rank(t.search_doc, websearch_to_tsquery('english', $1)) DESC, t.score DESC
		LIMIT $3`, q, "%"+escapeLike(q)+"%", limit)
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

// CreateComment adds a comment (a reply when parentID > 0) and bumps the
// thread's counters. It returns the new comment's id.
func (s *Store) CreateComment(ctx context.Context, threadID, parentID, authorID int64, body string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	depth, parentPath := 0, ""
	var parent sql.NullInt64
	if parentID > 0 {
		var parentThread int64
		err := tx.QueryRowContext(ctx, `SELECT thread_id, depth, path FROM comments WHERE id = $1`, parentID).
			Scan(&parentThread, &depth, &parentPath)
		if err != nil {
			return 0, notFound(err)
		}
		if parentThread != threadID {
			return 0, ErrWrongThread
		}
		depth++
		if depth > MaxCommentDepth {
			return 0, ErrCommentTooDeep
		}
		parent = sql.NullInt64{Int64: parentID, Valid: true}
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE threads SET comment_count = comment_count + 1, last_activity_at = now() WHERE id = $1`, threadID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}

	// The path needs the new id, so insert with a placeholder and set it.
	var id int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO comments (thread_id, parent_id, author_id, body_md, depth, path)
		VALUES ($1, $2, $3, $4, $5, '') RETURNING id`,
		threadID, parent, authorID, body, depth).Scan(&id); err != nil {
		return 0, err
	}
	path := fmt.Sprintf("%010d", id)
	if parentPath != "" {
		path = parentPath + "/" + path
	}
	if _, err := tx.ExecContext(ctx, `UPDATE comments SET path = $2 WHERE id = $1`, id, path); err != nil {
		return 0, err
	}
	if err := s.notifyReply(ctx, tx, threadID, parentID, id, authorID); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// CommentsByThread returns the whole tree in depth-first order.
func (s *Store) CommentsByThread(ctx context.Context, threadID int64) ([]*Comment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.thread_id, c.parent_id, c.author_id, c.body_md, c.score, c.depth, c.created_at,
			u.username, u.display_name, u.reputation
		FROM comments c JOIN users u ON u.id = c.author_id
		WHERE c.thread_id = $1 ORDER BY c.path`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Comment
	for rows.Next() {
		c := &Comment{}
		if err := rows.Scan(&c.ID, &c.ThreadID, &c.ParentID, &c.AuthorID, &c.BodyMD, &c.Score, &c.Depth,
			&c.CreatedAt, &c.AuthorUsername, &c.AuthorDisplayName, &c.AuthorReputation); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) CommentByID(ctx context.Context, id int64) (*Comment, error) {
	c := &Comment{}
	err := s.db.QueryRowContext(ctx, `
		SELECT c.id, c.thread_id, c.parent_id, c.author_id, c.body_md, c.score, c.depth, c.created_at,
			u.username, u.display_name, u.reputation
		FROM comments c JOIN users u ON u.id = c.author_id WHERE c.id = $1`, id).
		Scan(&c.ID, &c.ThreadID, &c.ParentID, &c.AuthorID, &c.BodyMD, &c.Score, &c.Depth,
			&c.CreatedAt, &c.AuthorUsername, &c.AuthorDisplayName, &c.AuthorReputation)
	if err != nil {
		return nil, notFound(err)
	}
	return c, nil
}

// UserVotes returns the voter's current votes on a thread and its comments,
// keyed "thread:12" / "comment:34" -> +1/-1, for highlighting vote buttons.
func (s *Store) UserVotes(ctx context.Context, voterID, threadID int64) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT target_type, target_id, value FROM votes
		WHERE voter_id = $1 AND (
			(target_type = 'thread' AND target_id = $2) OR
			(target_type = 'comment' AND target_id IN (SELECT id FROM comments WHERE thread_id = $2)))`,
		voterID, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var typ string
		var id int64
		var v int
		if err := rows.Scan(&typ, &id, &v); err != nil {
			return nil, err
		}
		out[fmt.Sprintf("%s:%d", typ, id)] = v
	}
	return out, rows.Err()
}

// VoteResult is the target's state after a vote.
type VoteResult struct {
	Score     float64
	Value     int     // the voter's vote now: -1, 0 or +1
	Weight    float64 // weight applied (0 when retracting)
	RepChange int     // reputation moved for the author by this call
}

// Vote sets voterID's vote on a thread or comment to value (+1, -1, or 0 to
// retract). In one transaction it:
//   - locks the target and its author, so score and reputation stay consistent
//     under concurrent votes;
//   - undoes the previous vote's score effect and reputation (as a
//     compensating ledger entry, never an edit);
//   - snapshots the voter's current weight, applies value*weight to the score,
//     and records the capped reputation reward in the ledger.
//
// Re-sending the same vote is a no-op, so double submits are harmless.
func (s *Store) Vote(ctx context.Context, voterID int64, targetType string, targetID int64, value int) (VoteResult, error) {
	table := map[string]string{"thread": "threads", "comment": "comments"}[targetType]
	if table == "" || value < -1 || value > 1 {
		return VoteResult{}, ErrBadVoteTarget
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return VoteResult{}, err
	}
	defer tx.Rollback()

	var authorID int64
	var score float64
	err = tx.QueryRowContext(ctx, `SELECT author_id, score FROM `+table+` WHERE id = $1 FOR UPDATE`, targetID).
		Scan(&authorID, &score)
	if err != nil {
		return VoteResult{}, notFound(err)
	}
	if authorID == voterID {
		return VoteResult{}, ErrSelfVote
	}

	var voterRep int
	var voterCreated time.Time
	if err := tx.QueryRowContext(ctx, `SELECT reputation, created_at FROM users WHERE id = $1`, voterID).
		Scan(&voterRep, &voterCreated); err != nil {
		return VoteResult{}, notFound(err)
	}
	if value < 0 && voterRep < rep.DownvoteMinRep {
		return VoteResult{}, ErrDownvoteLocked
	}
	// Serialise all reputation changes for this author.
	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, authorID); err != nil {
		return VoteResult{}, err
	}

	var oldValue int
	var oldWeight float64
	err = tx.QueryRowContext(ctx, `
		SELECT value, weight FROM votes WHERE voter_id = $1 AND target_type = $2 AND target_id = $3`,
		voterID, targetType, targetID).Scan(&oldValue, &oldWeight)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return VoteResult{}, err
	}
	if oldValue == value {
		return VoteResult{Score: score, Value: value, Weight: oldWeight}, nil
	}

	repChange := 0
	if oldValue != 0 {
		score -= float64(oldValue) * oldWeight
		// Reverse whatever this voter's vote on this target has netted so far.
		var net int
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(sum(delta), 0) FROM rep_events
			WHERE user_id = $1 AND source_user_id = $2 AND target_type = $3 AND target_id = $4`,
			authorID, voterID, targetType, targetID).Scan(&net); err != nil {
			return VoteResult{}, err
		}
		if net != 0 {
			if err := addRep(ctx, tx, authorID, -net, "vote_retracted", voterID, targetType, targetID); err != nil {
				return VoteResult{}, err
			}
			repChange -= net
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM votes WHERE voter_id = $1 AND target_type = $2 AND target_id = $3`,
			voterID, targetType, targetID); err != nil {
			return VoteResult{}, err
		}
	}

	weight := 0.0
	if value != 0 {
		weight = rep.Weight(voterRep, time.Since(voterCreated))
		score += float64(value) * weight
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO votes (voter_id, target_type, target_id, value, weight) VALUES ($1, $2, $3, $4, $5)`,
			voterID, targetType, targetID, value, weight); err != nil {
			return VoteResult{}, err
		}

		reward := rep.Reward(targetType, value, weight)
		if reward > 0 {
			var fromVoter, total int
			if err := tx.QueryRowContext(ctx, `
				SELECT COALESCE(sum(delta) FILTER (WHERE source_user_id = $2), 0),
				       COALESCE(sum(delta), 0)
				FROM rep_events WHERE user_id = $1 AND created_at > now() - interval '1 day'`,
				authorID, voterID).Scan(&fromVoter, &total); err != nil {
				return VoteResult{}, err
			}
			reward = rep.Capped(reward, fromVoter, total)
		}
		if reward != 0 {
			reason := "upvote_received"
			if value < 0 {
				reason = "downvote_received"
			}
			if err := addRep(ctx, tx, authorID, reward, reason, voterID, targetType, targetID); err != nil {
				return VoteResult{}, err
			}
			repChange += reward
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET score = $2 WHERE id = $1`, targetID, score); err != nil {
		return VoteResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return VoteResult{}, err
	}
	return VoteResult{Score: score, Value: value, Weight: weight, RepChange: repChange}, nil
}

// addRep appends to the ledger and updates the cached total.
func addRep(ctx context.Context, tx *sql.Tx, userID int64, delta int, reason string, sourceID int64, targetType string, targetID int64) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO rep_events (user_id, delta, reason, source_user_id, target_type, target_id)
		VALUES ($1, $2, $3, $4, $5, $6)`, userID, delta, reason, sourceID, targetType, targetID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE users SET reputation = reputation + $2 WHERE id = $1`, userID, delta)
	return err
}

// RebuildReputation recomputes every user's cached reputation from the
// ledger and returns how many users changed. The ledger is authoritative.
func (s *Store) RebuildReputation(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		WITH totals AS (
			SELECT u.id, COALESCE(sum(r.delta), 0)::int AS total
			FROM users u LEFT JOIN rep_events r ON r.user_id = u.id
			GROUP BY u.id
		)
		UPDATE users u SET reputation = t.total
		FROM totals t WHERE t.id = u.id AND u.reputation <> t.total`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RepEvent is one ledger line, for the user's reputation history page.
type RepEvent struct {
	Delta      int
	Reason     string // upvote_received, downvote_received, vote_retracted
	TargetType string // "thread", "comment" or "" (e.g. a manual adjustment)
	TargetID   int64
	CreatedAt  time.Time

	// The thread the target lives in; zero / empty if it was deleted.
	ThreadID    int64
	ThreadTitle string
}

func (s *Store) RepHistory(ctx context.Context, userID int64, limit int) ([]RepEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.delta, r.reason, COALESCE(r.target_type, ''), COALESCE(r.target_id, 0), r.created_at,
		       COALESCE(t.id, 0), COALESCE(t.title, '')
		FROM rep_events r
		LEFT JOIN comments c ON r.target_type = 'comment' AND c.id = r.target_id
		LEFT JOIN threads t ON t.id = CASE WHEN r.target_type = 'thread' THEN r.target_id ELSE c.thread_id END
		WHERE r.user_id = $1 ORDER BY r.id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepEvent
	for rows.Next() {
		var e RepEvent
		if err := rows.Scan(&e.Delta, &e.Reason, &e.TargetType, &e.TargetID, &e.CreatedAt,
			&e.ThreadID, &e.ThreadTitle); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
