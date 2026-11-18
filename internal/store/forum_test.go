package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/chenbenny31/plurb/internal/db/dbtest"
	"github.com/chenbenny31/plurb/internal/rep"
	"github.com/chenbenny31/plurb/internal/store"
)

type forum struct {
	t  *testing.T
	db *sql.DB
	st *store.Store
}

func newForum(t *testing.T) *forum {
	pool := dbtest.New(t)
	return &forum{t: t, db: pool, st: store.New(pool)}
}

// user creates an account of the given age with a starting reputation,
// recorded in the ledger so RebuildReputation agrees with it.
func (f *forum) user(name string, reputation int, age time.Duration) int64 {
	f.t.Helper()
	var id int64
	err := f.db.QueryRow(`INSERT INTO users (username, display_name, password_hash, created_at, reputation)
		VALUES ($1, $1, '', now() - $2 * interval '1 second', $3) RETURNING id`,
		name, age.Seconds(), reputation).Scan(&id)
	if err != nil {
		f.t.Fatal(err)
	}
	if reputation != 0 {
		f.db.Exec(`INSERT INTO rep_events (user_id, delta, reason) VALUES ($1, $2, 'seed')`, id, reputation)
	}
	return id
}

func (f *forum) thread(author int64) int64 {
	f.t.Helper()
	id, err := f.st.CreateThread(context.Background(), author, "A thread", "body")
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *forum) rep(user int64) int {
	var r int
	f.db.QueryRow(`SELECT reputation FROM users WHERE id = $1`, user).Scan(&r)
	return r
}

func (f *forum) vote(voter int64, typ string, target int64, value int) store.VoteResult {
	f.t.Helper()
	res, err := f.st.Vote(context.Background(), voter, typ, target, value)
	if err != nil {
		f.t.Fatalf("vote %d on %s %d = %d: %v", voter, typ, target, value, err)
	}
	return res
}

const old = 30 * 24 * time.Hour

func TestVoteLifecycle(t *testing.T) {
	f := newForum(t)
	author := f.user("author", 0, old)
	voter := f.user("voter", 20, old) // enough to downvote
	th := f.thread(author)
	w := rep.Weight(20, old)

	res := f.vote(voter, "thread", th, 1)
	if res.Score != w || f.rep(author) != rep.ThreadUpvote {
		t.Fatalf("upvote: score %v rep %d", res.Score, f.rep(author))
	}
	if res := f.vote(voter, "thread", th, 1); res.Score != w || f.rep(author) != rep.ThreadUpvote {
		t.Fatal("repeating a vote should be a no-op")
	}

	res = f.vote(voter, "thread", th, -1)
	if res.Score != -w || f.rep(author) != rep.DownvoteReward {
		t.Fatalf("flip to downvote: score %v rep %d", res.Score, f.rep(author))
	}

	res = f.vote(voter, "thread", th, 0)
	if res.Score != 0 || f.rep(author) != 0 {
		t.Fatalf("retract: score %v rep %d", res.Score, f.rep(author))
	}
	var rows int
	f.db.QueryRow(`SELECT count(*) FROM rep_events WHERE user_id = $1`, author).Scan(&rows)
	if rows != 4 { // +5, -5 (retract), -1, +1 (retract)
		t.Fatalf("ledger rows for author = %d, want 4 (history is appended, not edited)", rows)
	}
}

func TestVoteRules(t *testing.T) {
	f := newForum(t)
	author := f.user("author", 0, old)
	newbie := f.user("newbie", 0, time.Hour)
	lowRep := f.user("lowrep", 0, old)
	th := f.thread(author)

	if _, err := f.st.Vote(context.Background(), author, "thread", th, 1); !errors.Is(err, store.ErrSelfVote) {
		t.Errorf("self vote: %v", err)
	}
	if _, err := f.st.Vote(context.Background(), lowRep, "thread", th, -1); !errors.Is(err, store.ErrDownvoteLocked) {
		t.Errorf("low-rep downvote: %v", err)
	}
	if _, err := f.st.Vote(context.Background(), lowRep, "thread", 99999, 1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing target: %v", err)
	}

	res := f.vote(newbie, "thread", th, 1)
	if res.Score != 0 || res.Weight != 0 || f.rep(author) != 0 {
		t.Errorf("new account vote should weigh 0: %+v rep %d", res, f.rep(author))
	}

	whale := f.user("whale", 300, old) // weight 3
	if res := f.vote(whale, "thread", th, 1); math.Abs(res.Score-3) > 1e-9 {
		t.Errorf("weighted vote: score %v, want 3", res.Score)
	}
}

func TestPerVoterDailyCap(t *testing.T) {
	f := newForum(t)
	author := f.user("author", 0, old)
	friend := f.user("friend", 0, old)
	for i := 0; i < 3; i++ {
		f.vote(friend, "thread", f.thread(author), 1)
	}
	if got := f.rep(author); got != rep.PerVoterDailyCap {
		t.Fatalf("rep from one voter in a day = %d, want cap %d", got, rep.PerVoterDailyCap)
	}
	// Scores still count every vote; only reputation is capped.
	var total float64
	f.db.QueryRow(`SELECT sum(score) FROM threads WHERE author_id = $1`, author).Scan(&total)
	if total != 3 {
		t.Fatalf("scores = %v, want 3", total)
	}
}

func TestConcurrentVotesStayConsistent(t *testing.T) {
	f := newForum(t)
	author := f.user("author", 0, old)
	th := f.thread(author)
	const n = 25
	voters := make([]int64, n)
	for i := range voters {
		voters[i] = f.user(fmt.Sprintf("voter%d", i), i*40, old)
	}

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for _, v := range voters {
		wg.Add(1)
		go func(v int64) {
			defer wg.Done()
			if _, err := f.st.Vote(context.Background(), v, "thread", th, 1); err != nil {
				errs <- err
			}
		}(v)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	var score, sumWeights float64
	f.db.QueryRow(`SELECT score FROM threads WHERE id = $1`, th).Scan(&score)
	f.db.QueryRow(`SELECT sum(weight) FROM votes WHERE target_type = 'thread' AND target_id = $1`, th).Scan(&sumWeights)
	if math.Abs(score-sumWeights) > 1e-9 {
		t.Fatalf("score %v != sum of vote weights %v (lost update)", score, sumWeights)
	}
	var ledger int
	f.db.QueryRow(`SELECT COALESCE(sum(delta), 0) FROM rep_events WHERE user_id = $1`, author).Scan(&ledger)
	if got := f.rep(author); got != ledger || got != n*rep.ThreadUpvote {
		t.Fatalf("cached rep %d, ledger %d, want %d", got, ledger, n*rep.ThreadUpvote)
	}
}

func TestLedgerIsAppendOnlyAndRebuildable(t *testing.T) {
	f := newForum(t)
	author := f.user("author", 0, old)
	voter := f.user("voter", 0, old)
	f.vote(voter, "thread", f.thread(author), 1)

	if _, err := f.db.Exec(`UPDATE rep_events SET delta = 1000`); err == nil {
		t.Fatal("ledger rows were editable")
	}
	f.db.Exec(`UPDATE users SET reputation = 999 WHERE id = $1`, author) // drift the cache
	if n, err := f.st.RebuildReputation(context.Background()); err != nil || n != 1 {
		t.Fatalf("rebuild: n=%d err=%v", n, err)
	}
	if got := f.rep(author); got != rep.ThreadUpvote {
		t.Fatalf("rebuilt rep = %d", got)
	}
	// Deleting a voter keeps the author's earned history.
	if _, err := f.db.Exec(`DELETE FROM users WHERE id = $1`, voter); err != nil {
		t.Fatalf("deleting a voter: %v", err)
	}
}

func TestCommentTree(t *testing.T) {
	f := newForum(t)
	ctx := context.Background()
	u := f.user("commenter", 0, old)
	th := f.thread(u)
	other := f.thread(u)

	a, _ := f.st.CreateComment(ctx, th, 0, u, "a")
	f.st.CreateComment(ctx, th, 0, u, "b")
	a1, _ := f.st.CreateComment(ctx, th, a, u, "a1")
	f.st.CreateComment(ctx, th, a1, u, "a1x")

	cs, err := f.st.CommentsByThread(ctx, th)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, c := range cs {
		order = append(order, fmt.Sprintf("%s@%d", c.BodyMD, c.Depth))
	}
	if got := fmt.Sprint(order); got != "[a@0 a1@1 a1x@2 b@0]" {
		t.Fatalf("tree order = %s", got)
	}

	if _, err := f.st.CreateComment(ctx, other, a, u, "x"); !errors.Is(err, store.ErrWrongThread) {
		t.Errorf("cross-thread reply: %v", err)
	}
	parent := a1
	for i := 0; i < store.MaxCommentDepth; i++ {
		id, err := f.st.CreateComment(ctx, th, parent, u, "deep")
		if err != nil {
			if !errors.Is(err, store.ErrCommentTooDeep) {
				t.Fatal(err)
			}
			break
		}
		parent = id
	}
	if _, err := f.st.CreateComment(ctx, th, parent, u, "too deep"); !errors.Is(err, store.ErrCommentTooDeep) {
		t.Errorf("depth limit: %v", err)
	}
	var rows int
	f.db.QueryRow(`SELECT count(*) FROM comments WHERE thread_id = $1`, th).Scan(&rows)
	if thr, _ := f.st.ThreadByID(ctx, th); thr.CommentCount != rows {
		t.Errorf("comment_count = %d, rows = %d", thr.CommentCount, rows)
	}
}

func TestReplyNotifications(t *testing.T) {
	f := newForum(t)
	ctx := context.Background()
	alice := f.user("alice", 0, old)
	bob := f.user("bob", 0, old)
	th := f.thread(alice)

	top, _ := f.st.CreateComment(ctx, th, 0, bob, "bob on alice's thread") // -> alice
	f.st.CreateComment(ctx, th, top, alice, "alice replies to bob")        // -> bob
	f.st.CreateComment(ctx, th, 0, alice, "alice on her own thread")       // -> nobody

	if n, _ := f.st.UnreadCount(ctx, alice); n != 1 {
		t.Errorf("alice unread = %d, want 1", n)
	}
	got, _ := f.st.Notifications(ctx, bob, 10)
	if len(got) != 1 || got[0].Kind != "comment_reply" || got[0].ActorDisplayName != "alice" {
		t.Fatalf("bob's notifications: %+v", got)
	}
	f.st.MarkNotificationsRead(ctx, bob)
	if n, _ := f.st.UnreadCount(ctx, bob); n != 0 {
		t.Errorf("bob unread after mark read = %d", n)
	}
}

func TestRepLevelsFromDistribution(t *testing.T) {
	f := newForum(t)
	ctx := context.Background()
	empty, err := f.st.RepLevels(ctx)
	if err != nil || empty.Users != 0 {
		t.Fatalf("empty distribution: %+v %v", empty, err)
	}
	// 100 users with reputation 1..100, plus one with none.
	for i := 1; i <= 100; i++ {
		f.user(fmt.Sprintf("user%03d", i), i, old)
	}
	f.user("lurker", 0, old)

	l, err := f.st.RepLevels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if l.Users != 100 || l.Cuts != [4]int{50, 80, 95, 99} {
		t.Fatalf("levels = %+v", l)
	}
	if top, _ := f.st.RepTopPercent(ctx, 95); top != 6 {
		t.Errorf("rep 95 ranks top %d%%, want 6%%", top)
	}
	if top, _ := f.st.RepTopPercent(ctx, 100); top != 1 {
		t.Errorf("highest rep ranks top %d%%, want 1%%", top)
	}
}
