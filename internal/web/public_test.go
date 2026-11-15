package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// seedPost inserts a post directly (the editor arrives in a later layer).
// Published posts get increasing timestamps so ordering is deterministic.
func (a *testApp) seedPost(author, slug, title, body string, published bool, tags ...string) int64 {
	a.t.Helper()
	ctx := context.Background()
	u, err := a.srv.store.UserByUsername(ctx, author)
	if err != nil {
		a.t.Fatal(err)
	}
	status := "draft"
	pubAt := "NULL"
	if published {
		status = "published"
		pubAt = "now() - interval '1 day' + (SELECT count(*) FROM posts) * interval '1 minute'"
	}
	var id int64
	err = a.srv.db.QueryRowContext(ctx, `INSERT INTO posts (author_id, slug, title, body_md, status, published_at)
		VALUES ($1, $2, $3, $4, $5, `+pubAt+`) RETURNING id`, u.ID, slug, title, body, status).Scan(&id)
	if err != nil {
		a.t.Fatal(err)
	}
	for _, tag := range tags {
		_, err := a.srv.db.ExecContext(ctx, `
			WITH t AS (INSERT INTO tags (slug, name) VALUES ($1, $1)
			           ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name RETURNING id)
			INSERT INTO post_tags (post_id, tag_id) SELECT $2, id FROM t`, tag, id)
		if err != nil {
			a.t.Fatal(err)
		}
	}
	return id
}

func mustContain(t *testing.T, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("page missing %q", w)
		}
	}
}

func mustNotContain(t *testing.T, body string, banned ...string) {
	t.Helper()
	for _, b := range banned {
		if strings.Contains(body, b) {
			t.Errorf("page unexpectedly contains %q", b)
		}
	}
}

func TestPanoramaIndex(t *testing.T) {
	a := newTestApp(t, nil)
	a.createUser("benny", "correct horse battery")
	a.seedPost("benny", "go-intro", "Intro to Go", "hello", true, "go")
	a.seedPost("benny", "pg-notes", "Postgres Notes", "hello", true, "databases")
	a.seedPost("benny", "secret", "Secret Draft", "wip", false, "go")

	resp, body := a.get("/panorama")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	mustContain(t, body, "Intro to Go", "Postgres Notes", `href="/panorama?tag=go"`)
	mustNotContain(t, body, "Secret Draft")

	_, body = a.get("/panorama?tag=databases")
	mustContain(t, body, "Postgres Notes")
	mustNotContain(t, body, "Intro to Go")

	for i := 0; i < 10; i++ {
		a.seedPost("benny", fmt.Sprintf("p%d", i), fmt.Sprintf("Filler %d", i), "x", true)
	}
	_, body = a.get("/panorama")
	mustContain(t, body, `href="/panorama?page=2"`)
	_, body = a.get("/panorama?page=2")
	mustContain(t, body, "Intro to Go") // oldest post lands on page 2
}

func TestPostPage(t *testing.T) {
	a := newTestApp(t, nil)
	a.createUser("benny", "correct horse battery")
	a.seedPost("benny", "first", "First", "one", true, "go")
	body := "## A\n\n## B\n\n## C\n\n<script>alert(1)</script>"
	a.seedPost("benny", "second", "Second <b>bold</b>", body, true, "go")
	a.seedPost("benny", "third", "Third", "three", true)
	a.seedPost("benny", "draft", "Draft", "wip", false)

	resp, page := a.get("/panorama/benny/second")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	mustContain(t, page,
		"Second &lt;b&gt;bold&lt;/b&gt;", // title escaped
		`class="toc"`, `href="#a"`,       // 3 headings -> TOC
		"← First", "Third →", // neighbours
		`class="related"`, // shares the go tag with First
		`property="og:url" content="http://localhost/panorama/benny/second"`)
	mustNotContain(t, page, "<script>alert(1)")

	if resp, _ := a.get("/panorama/benny/draft"); resp.StatusCode != 404 {
		t.Errorf("draft visible to anonymous: %d", resp.StatusCode)
	}
	a.login("benny", "correct horse battery")
	resp, page = a.get("/panorama/benny/draft")
	if resp.StatusCode != 200 || resp.Header.Get("X-Robots-Tag") != "noindex" {
		t.Errorf("draft for owner: %d", resp.StatusCode)
	}
	mustContain(t, page, "Only you can see this page")

	if resp, _ := a.get("/panorama/nobody/first"); resp.StatusCode != 404 {
		t.Errorf("unknown author: %d", resp.StatusCode)
	}
}

func TestViewCounting(t *testing.T) {
	a := newTestApp(t, nil)
	a.createUser("benny", "correct horse battery")
	id := a.seedPost("benny", "hello", "Hello", "x", true)

	views := func() (n int) {
		a.srv.db.QueryRow(`SELECT view_count FROM posts WHERE id = $1`, id).Scan(&n)
		return n
	}
	ua := map[string]string{"User-Agent": "Mozilla/5.0"}
	a.do("GET", "/panorama/benny/hello", nil, ua)
	a.do("GET", "/panorama/benny/hello", nil, ua)
	if n := views(); n != 1 {
		t.Fatalf("repeat visitor counted %d times", n)
	}
	a.do("GET", "/panorama/benny/hello", nil, map[string]string{"User-Agent": "Googlebot/2.1"})
	a.login("benny", "correct horse battery")
	a.do("GET", "/panorama/benny/hello", nil, map[string]string{"User-Agent": "Mozilla/5.0 (other)"})
	if n := views(); n != 1 {
		t.Fatalf("bot or owner counted: %d", n)
	}
}

func TestSearch(t *testing.T) {
	a := newTestApp(t, nil)
	a.createUser("benny", "correct horse battery")
	a.seedPost("benny", "pg", "Indexing", "How databases choose an index.", true)
	a.seedPost("benny", "zh", "Go 笔记", "goroutine 与并发模型。", true)
	a.seedPost("benny", "hidden", "Draft about databases", "databases", false)

	_, body := a.get("/panorama/search?q=database") // stemmed match
	mustContain(t, body, "Indexing", "1 result(s)")
	mustNotContain(t, body, "Draft about databases")

	_, body = a.get("/panorama/search?q=" + "%E5%B9%B6%E5%8F%91") // 并发, substring match
	mustContain(t, body, "Go 笔记")

	_, body = a.get("/panorama/search?q=100%25_off") // LIKE wildcards are literal
	mustContain(t, body, "No results")
}

func TestProfile(t *testing.T) {
	a := newTestApp(t, nil)
	a.createUser("benny", "correct horse battery")
	a.srv.db.Exec(`UPDATE users SET about_md = '# Hi\n\nI write **Go**.' WHERE username = 'benny'`)
	a.seedPost("benny", "hello", "Hello World", "x", true, "go")

	_, body := a.get("/panorama/benny")
	mustContain(t, body, "Hello World", "@benny", `href="/panorama/benny/tag/go"`)
	_, body = a.get("/panorama/benny/about")
	mustContain(t, body, "<strong>Go</strong>")

	resp, body := a.get("/panorama/benny/tag/go")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tag page: %d", resp.StatusCode)
	}
	mustContain(t, body, "Posts tagged go", "Hello World")
	if resp, _ := a.get("/panorama/benny/tag/nope"); resp.StatusCode != 404 {
		t.Errorf("unknown tag: %d", resp.StatusCode)
	}
}
