package web

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestLevelsAreLazyAndRelative(t *testing.T) {
	a := newTestApp(t, nil)
	a.srv.lv.ttl = time.Hour
	// 100 contributors with 1..100 points; "star" has 100.
	for i := 1; i <= 99; i++ {
		a.srv.db.Exec(`INSERT INTO users (username, display_name, password_hash, reputation)
			VALUES ($1, $1, '', $2)`, fmt.Sprintf("member%02d", i), i)
	}
	a.srv.db.Exec(`INSERT INTO users (username, display_name, password_hash, reputation)
		VALUES ('star', 'Star', '', 600)`)

	badge := func() string {
		_, page := a.get("/panorama/star")
		i := strings.Index(page, `<span class="lv lv-`)
		if i < 0 {
			t.Fatal("no level badge on profile")
		}
		return page[i+len(`<span class="lv lv-`) : i+len(`<span class="lv lv-`)+1]
	}
	if got := badge(); got != "5" {
		t.Fatalf("top 1%% with 600 points: level %s, want 5", got)
	}

	// Everyone else overtakes star; within the TTL the cached cut points
	// still apply (lazy), after it they're recomputed (relative).
	a.srv.db.Exec(`UPDATE users SET reputation = reputation + 10000 WHERE username <> 'star'`)
	if got := badge(); got != "5" {
		t.Fatalf("within TTL the snapshot should be reused: level %s", got)
	}
	a.srv.lv.mu.Lock()
	a.srv.lv.at = time.Now().Add(-2 * time.Hour)
	a.srv.lv.mu.Unlock()
	if got := badge(); got != "1" {
		t.Fatalf("after TTL star is now the lowest: level %s, want 1", got)
	}
}
