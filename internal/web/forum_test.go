package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// matureUser creates an account old enough for its votes to carry weight.
func (a *testApp) matureUser(name, password string, reputation int) {
	a.t.Helper()
	a.createUser(name, password)
	a.srv.db.Exec(`UPDATE users SET created_at = now() - interval '30 days', reputation = $2 WHERE username = $1`,
		name, reputation)
}

func (a *testApp) newThread(title, body string) string {
	a.t.Helper()
	resp, _ := a.post("/forum/new", url.Values{"title": {title}, "body": {body}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		a.t.Fatalf("new thread: %d", resp.StatusCode)
	}
	return resp.Header.Get("Location") // /forum/t/{id}
}

func TestForumBrowseAndPost(t *testing.T) {
	a := newTestApp(t, nil)
	if resp, _ := a.get("/forum"); resp.StatusCode != 200 {
		t.Fatalf("forum index: %d", resp.StatusCode)
	}
	if resp, _ := a.post("/forum/new", url.Values{"title": {"x"}}, nil); resp.StatusCode != http.StatusSeeOther ||
		!strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Fatalf("anonymous post should go to login: %d", resp.StatusCode)
	}

	a.matureUser("alice", "alice password", 0)
	a.login("alice", "alice password")
	loc := a.newThread("Why <b>Go</b>?", "Because **channels**. <script>x</script>")
	_, page := a.get(loc)
	mustContain(t, page, "Why &lt;b&gt;Go&lt;/b&gt;?", "<strong>channels</strong>")
	mustNotContain(t, page, "<script>x")

	id := strings.TrimPrefix(loc, "/forum/t/")
	resp, _ := a.post(loc+"/comments", url.Values{"body": {"top-level"}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("comment: %d", resp.StatusCode)
	}
	parent := strings.TrimPrefix(resp.Header.Get("Location"), loc+"#c")
	a.post(loc+"/comments", url.Values{"body": {"a reply"}, "parent_id": {parent}}, nil)
	_, page = a.get(loc)
	mustContain(t, page, `class="comment depth-0"`, `class="comment depth-1"`, "2 comments")

	if resp, _ := a.post(loc+"/comments", url.Values{"body": {"  "}}, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("empty comment: %d", resp.StatusCode)
	}
	_, page = a.get("/forum/search?q=channels")
	mustContain(t, page, `href="/forum/t/`+id+`"`)
}

func vote(a *testApp, typ, id, value string) (*http.Response, voteResponse) {
	a.t.Helper()
	resp, body := a.post("/forum/vote", url.Values{"type": {typ}, "id": {id}, "value": {value}, "thread": {id}},
		map[string]string{"Accept": "application/json"})
	var vr voteResponse
	json.Unmarshal([]byte(body), &vr)
	return resp, vr
}

func TestForumVoting(t *testing.T) {
	a := newTestApp(t, nil)
	a.matureUser("alice", "alice password", 0)
	a.matureUser("bob", "bob password", 300) // weight 3, may downvote
	a.matureUser("carol", "carol password", 0)

	a.login("alice", "alice password")
	id := strings.TrimPrefix(a.newThread("Weighted votes", "x"), "/forum/t/")
	if resp, vr := vote(a, "thread", id, "1"); resp.StatusCode != http.StatusUnprocessableEntity ||
		!strings.Contains(vr.Error, "own post") {
		t.Fatalf("self vote: %d %+v", resp.StatusCode, vr)
	}

	bob := newClientFor(a)
	bob.login("bob", "bob password")
	if _, vr := vote(bob, "thread", id, "1"); vr.Score != "3" || vr.Value != 1 {
		t.Fatalf("bob upvote: %+v", vr)
	}

	carol := newClientFor(a)
	carol.login("carol", "carol password")
	if resp, vr := vote(carol, "thread", id, "-1"); resp.StatusCode != http.StatusUnprocessableEntity ||
		!strings.Contains(vr.Error, "reputation") {
		t.Fatalf("low-rep downvote: %d %+v", resp.StatusCode, vr)
	}
	if _, vr := vote(carol, "thread", id, "1"); vr.Score != "4" {
		t.Fatalf("carol upvote: %+v", vr)
	}

	// Form fallback (no JS) redirects back to the thread.
	resp, _ := bob.post("/forum/vote", url.Values{"type": {"thread"}, "id": {id}, "value": {"0"}, "thread": {id}}, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/forum/t/"+id {
		t.Fatalf("form vote: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	_, page := a.get("/forum/t/" + id)
	mustContain(t, page, `<span class="vote-score">1</span>`)
	_, page = a.get("/forum/reputation")
	mustContain(t, page, "upvote_received", "vote_retracted")
	_, page = a.get("/panorama/alice")
	mustContain(t, page, "@alice · 5 reputation") // bob's +5 retracted, carol's +5 stands
}

func TestForumHotOrdering(t *testing.T) {
	a := newTestApp(t, nil)
	a.matureUser("alice", "alice password", 0)
	a.login("alice", "alice password")
	oldID := strings.TrimPrefix(a.newThread("Old but popular", "x"), "/forum/t/")
	newID := strings.TrimPrefix(a.newThread("Fresh", "x"), "/forum/t/")
	a.srv.db.Exec(`UPDATE threads SET score = 10, created_at = now() - interval '3 days' WHERE id = $1`, oldID)
	a.srv.db.Exec(`UPDATE threads SET score = 2 WHERE id = $1`, newID)

	_, hot := a.get("/forum?sort=hot")
	_, top := a.get("/forum?sort=top")
	if strings.Index(hot, "Fresh") > strings.Index(hot, "Old but popular") {
		t.Error("hot: a fresh thread with score 2 should outrank a 3-day-old one with score 10")
	}
	if strings.Index(top, "Old but popular") > strings.Index(top, "Fresh") {
		t.Error("top: higher score should come first")
	}
}
