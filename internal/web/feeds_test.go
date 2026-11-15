package web

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestFeeds(t *testing.T) {
	a := newTestApp(t, nil)
	a.createUser("benny", "correct horse battery")
	a.createUser("alice", "alice password")
	a.seedPost("benny", "hello", "Hello & <Welcome>", "**bold** <script>x</script>", true, "go")
	a.seedPost("alice", "hers", "Alice's post", "hi", true)
	a.seedPost("benny", "draft", "Draft", "wip", false)

	resp, body := a.get("/panorama/benny/feed.xml")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/rss+xml") {
		t.Fatalf("author feed: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var feed rss
	if err := xml.Unmarshal([]byte(body), &feed); err != nil {
		t.Fatalf("feed is not valid XML: %v", err)
	}
	if n := len(feed.Channel.Items); n != 1 {
		t.Fatalf("author feed has %d items, want 1 (no drafts, no other authors)", n)
	}
	item := feed.Channel.Items[0]
	if item.Title != "Hello & <Welcome>" || item.Link != "http://localhost/panorama/benny/hello" ||
		!strings.Contains(item.Description, "<strong>bold</strong>") || strings.Contains(item.Description, "<script") {
		t.Fatalf("item: %+v", item)
	}

	_, body = a.get("/panorama/feed.xml")
	var global rss
	if err := xml.Unmarshal([]byte(body), &global); err != nil || len(global.Channel.Items) != 2 {
		t.Fatalf("global feed: %d items, err %v", len(global.Channel.Items), err)
	}

	_, body = a.get("/sitemap.xml")
	mustContain(t, body, "<loc>http://localhost/panorama/benny/hello</loc>", "<loc>http://localhost/panorama/alice</loc>")
	mustNotContain(t, body, "/draft<")

	_, body = a.get("/robots.txt")
	mustContain(t, body, "Disallow: /panorama/editor", "Sitemap: http://localhost/sitemap.xml")
}

func TestExport(t *testing.T) {
	a := newTestApp(t, nil)
	a.createUser("benny", "correct horse battery")
	a.createUser("alice", "alice password")
	a.seedPost("benny", "hello", `Say "hi"`, "body one", true, "go")
	a.seedPost("benny", "wip", "WIP", "draft body", false)
	a.seedPost("alice", "hers", "Not mine", "x", true)
	a.login("benny", "correct horse battery")

	resp, body := a.get("/panorama/editor/export")
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Disposition"), "plurb-benny-") {
		t.Fatalf("export: %d %s", resp.StatusCode, resp.Header.Get("Content-Disposition"))
	}
	zr, err := zip.NewReader(bytes.NewReader([]byte(body)), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
	}
	if len(files) != 2 {
		t.Fatalf("files: %v", keys(files))
	}
	mustContain(t, files["hello.md"], `title: "Say \"hi\""`, `tags: ["go"]`, "status: published", "body one")
	mustContain(t, files["wip.md"], "status: draft", "draft body")
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
