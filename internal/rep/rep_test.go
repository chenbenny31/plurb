package rep

import (
	"testing"
	"time"
)

func TestWeight(t *testing.T) {
	day := 48 * time.Hour
	cases := []struct {
		rep  int
		age  time.Duration
		want float64
	}{
		{1000, time.Hour, 0}, // too new, however reputable
		{0, day, 1},
		{-50, day, 1}, // negative rep doesn't go below baseline
		{100, day, 2},
		{300, day, 3},
		{1_000_000, day, MaxWeight},
	}
	for _, c := range cases {
		if got := Weight(c.rep, c.age); got != c.want {
			t.Errorf("Weight(%d, %v) = %v, want %v", c.rep, c.age, got, c.want)
		}
	}
}

func TestRewardAndCaps(t *testing.T) {
	if Reward("thread", 1, 0) != 0 {
		t.Error("zero-weight vote moved reputation")
	}
	if Reward("thread", 1, 1) != ThreadUpvote || Reward("comment", 1, 2.5) != CommentUpvote || Reward("thread", -1, 1) != DownvoteReward {
		t.Error("base rewards")
	}
	if got := Capped(5, 8, 0); got != 2 {
		t.Errorf("per-voter cap: got %d, want 2", got)
	}
	if got := Capped(5, 0, 199); got != 1 {
		t.Errorf("per-author cap: got %d, want 1", got)
	}
	if got := Capped(5, 10, 0); got != 0 {
		t.Errorf("exhausted cap: got %d", got)
	}
	if got := Capped(-1, 100, 1000); got != -1 {
		t.Errorf("downvotes are never capped: got %d", got)
	}
}

func TestHotPrefersNewerAtEqualScore(t *testing.T) {
	if Hot(10, time.Hour) <= Hot(10, 24*time.Hour) {
		t.Error("newer thread should rank higher at equal score")
	}
	if Hot(50, 24*time.Hour) <= Hot(5, 24*time.Hour) {
		t.Error("higher score should rank higher at equal age")
	}
}

func TestLevels(t *testing.T) {
	// 100 users: cuts at the 50th/80th/95th/99th percentile.
	l := Levels{Cuts: [4]int{20, 120, 400, 900}, Users: 100}
	cases := []struct{ rep, want int }{
		{0, 0}, {-5, 0},
		{1, 1}, {19, 1},
		{20, 2}, {119, 2},
		{120, 3}, {399, 3},
		{400, 4}, {899, 4},
		{900, 5}, {5000, 5},
	}
	for _, c := range cases {
		if got := l.Of(c.rep); got != c.want {
			t.Errorf("Of(%d) = %d, want %d", c.rep, got, c.want)
		}
	}

	// A tiny community: the only active member has 5 points. The
	// distribution says "top 1%", the floor keeps them at level 1.
	tiny := Levels{Cuts: [4]int{5, 5, 5, 5}, Users: 1}
	if got := tiny.Of(5); got != 1 {
		t.Errorf("tiny community: Of(5) = %d, want 1", got)
	}
	if LevelName(0) != "New" || LevelName(3) != "Lv 3" {
		t.Error("level names")
	}
}
