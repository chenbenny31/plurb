package web

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestLoginRateLimit(t *testing.T) {
	a := newTestApp(t, nil)
	a.createUser("benny", "correct horse battery")
	bad := url.Values{"username": {"benny"}, "password": {"wrong password"}}
	for i := 0; i < loginPerUser; i++ {
		if resp, _ := a.post("/login", bad, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, resp.StatusCode)
		}
	}
	resp, body := a.post("/login", url.Values{"username": {"benny"}, "password": {"correct horse battery"}}, nil)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("over limit: %d Retry-After=%q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	mustContain(t, body, "Too many requests")
}

func TestRenderedPostCacheFollowsEdits(t *testing.T) {
	a := newTestApp(t, nil)
	a.createUser("benny", "correct horse battery")
	a.login("benny", "correct horse battery")
	a.post("/panorama/editor/new", formOf("Cached", "version **one**", "action", "publish"), nil)
	_, body := a.get("/panorama/benny/cached")
	mustContain(t, body, "version <strong>one</strong>")

	u, _ := a.srv.store.UserByUsername(context.Background(), "benny")
	p, _ := a.srv.store.PostBySlug(context.Background(), u.ID, "cached")
	time.Sleep(time.Millisecond) // updated_at must move for the new cache key
	a.post("/panorama/editor/posts/"+itoa(p.ID), formOf("Cached", "version **two**", "slug", "cached"), nil)
	_, body = a.get("/panorama/benny/cached")
	mustContain(t, body, "version <strong>two</strong>")
	mustNotContain(t, body, "version <strong>one</strong>")
}

func TestPopularSidebar(t *testing.T) {
	a := newTestApp(t, nil)
	a.createUser("benny", "correct horse battery")
	id := a.seedPost("benny", "hot", "Hot Post", "x", true)
	a.srv.db.Exec(`UPDATE posts SET view_count = 99 WHERE id = $1`, id)
	_, body := a.get("/panorama")
	mustContain(t, body, "<h3>Popular</h3>", `href="/panorama/benny/hot">Hot Post</a>`)
}
