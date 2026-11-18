package web

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

func newJar() (http.CookieJar, error) { return cookiejar.New(nil) }

func formOf(title, body string, extra ...string) url.Values {
	f := url.Values{"title": {title}, "body": {body}}
	for i := 0; i+1 < len(extra); i += 2 {
		f.Set(extra[i], extra[i+1])
	}
	return f
}

func TestEditorRequiresLogin(t *testing.T) {
	a := newTestApp(t, nil)
	resp, _ := a.get("/panorama/editor")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login?next=") {
		t.Fatalf("anonymous dashboard: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := a.post("/panorama/editor/new", formOf("x", "y"), nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("anonymous create: %d", resp.StatusCode)
	}
}

func TestCreateEditPublishDelete(t *testing.T) {
	a := newTestApp(t, nil)
	a.createUser("benny", "correct horse battery")
	a.login("benny", "correct horse battery")
	ctx := context.Background()

	resp, _ := a.post("/panorama/editor/new", formOf("Hello, World!", "first **draft**",
		"tags", "go Go #concurrency", "series", "Go Internals", "description", "hi"), nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create: %d", resp.StatusCode)
	}
	u, _ := a.srv.store.UserByUsername(ctx, "benny")
	p, err := a.srv.store.PostBySlug(ctx, u.ID, "hello-world")
	if err != nil {
		t.Fatalf("slug not derived from title: %v", err)
	}
	if p.Published() || len(p.Tags) != 2 || !p.SeriesID.Valid {
		t.Fatalf("created post: status=%s tags=%v series=%v", p.Status, p.Tags, p.SeriesID)
	}

	// Same title again gets a fresh slug.
	a.post("/panorama/editor/new", formOf("Hello, World!", "again"), nil)
	if _, err := a.srv.store.PostBySlug(ctx, u.ID, "hello-world-2"); err != nil {
		t.Fatalf("duplicate title slug: %v", err)
	}

	// Anonymous readers can't see the draft yet.
	edit := "/panorama/editor/posts/" + itoa(p.ID)
	resp, _ = a.post(edit, formOf("Hello, World!", "final", "slug", "hello-world", "action", "publish"), nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("update+publish: %d", resp.StatusCode)
	}
	p, _ = a.srv.store.PostByID(ctx, p.ID)
	if !p.Published() || p.BodyMD != "final" || len(p.Tags) != 0 || p.SeriesID.Valid {
		t.Fatalf("after update: status=%s body=%q tags=%v series=%v", p.Status, p.BodyMD, p.Tags, p.SeriesID)
	}
	firstPublished := *p.PublishedAt

	a.post(edit+"/publish", url.Values{}, nil) // unpublish
	a.post(edit+"/publish", url.Values{}, nil) // republish keeps the date
	p, _ = a.srv.store.PostByID(ctx, p.ID)
	if !p.Published() || !p.PublishedAt.Equal(firstPublished) {
		t.Fatalf("republish changed date: %v -> %v", firstPublished, p.PublishedAt)
	}

	if resp, _ := a.post(edit+"/delete", url.Values{}, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if _, err := a.srv.store.PostByID(ctx, p.ID); err == nil {
		t.Fatal("post still exists after delete")
	}
}

func TestEditorValidation(t *testing.T) {
	a := newTestApp(t, nil)
	a.createUser("benny", "correct horse battery")
	a.login("benny", "correct horse battery")

	cases := map[string]url.Values{
		"Cover image must be an https":  formOf("t", "b", "cover_url", "http://example.com/a.png"),
		"Slug may only use":             formOf("t", "b", "slug", "Bad Slug!"),
		"Title is required":             formOf("", "b"),
		"At most 10 tags":               formOf("t", "b", "tags", "a b c d e f g h i j k"),
		"Post body is too long (200 KB": formOf("t", strings.Repeat("x", 200_001)),
	}
	for want, form := range cases {
		resp, body := a.post("/panorama/editor/new", form, nil)
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, want) {
			t.Errorf("%s: got %d", want, resp.StatusCode)
		}
	}
	// Typed values survive a failed save.
	_, body := a.post("/panorama/editor/new", formOf("Keep me", "body text", "cover_url", "ftp://x"), nil)
	mustContain(t, body, `value="Keep me"`, ">body text</textarea>")

	a.post("/panorama/editor/new", formOf("One", "x", "slug", "taken"), nil)
	_, body = a.post("/panorama/editor/new", formOf("Two", "x", "slug", "taken"), nil)
	mustContain(t, body, "That slug is taken")
}

func TestCannotTouchOthersPosts(t *testing.T) {
	a := newTestApp(t, nil)
	a.createUser("alice", "alice password")
	a.createUser("mallory", "mallory password")
	id := a.seedPost("alice", "mine", "Mine", "original", true)
	a.login("mallory", "mallory password")

	edit := "/panorama/editor/posts/" + itoa(id)
	for _, path := range []string{edit, edit + "/publish", edit + "/delete"} {
		if resp, _ := a.post(path, formOf("Pwned", "pwned"), nil); resp.StatusCode != 404 {
			t.Errorf("POST %s by non-owner: %d", path, resp.StatusCode)
		}
	}
	if resp, _ := a.get(edit); resp.StatusCode != 404 {
		t.Errorf("GET edit page by non-owner: %d", resp.StatusCode)
	}
	p, _ := a.srv.store.PostByID(context.Background(), id)
	if p.BodyMD != "original" || !p.Published() {
		t.Fatal("non-owner modified the post")
	}
}

func TestPreviewIsSanitised(t *testing.T) {
	a := newTestApp(t, nil)
	a.createUser("benny", "correct horse battery")
	a.login("benny", "correct horse battery")
	_, body := a.post("/panorama/editor/preview", url.Values{"body": {"**hi** <script>x</script>"}}, nil)
	mustContain(t, body, "<strong>hi</strong>")
	mustNotContain(t, body, "<script")
}

func TestAboutAndAccount(t *testing.T) {
	a := newTestApp(t, nil)
	a.createUser("benny", "correct horse battery")
	a.createUser("taken", "some password!")
	a.login("benny", "correct horse battery")

	a.post("/panorama/editor/about", url.Values{"display_name": {"Benny C"}, "about": {"I like *Go*."}}, nil)
	_, body := a.get("/panorama/benny/about")
	mustContain(t, body, "Benny C", "<em>Go</em>")

	_, body = a.post("/panorama/editor/account/username", url.Values{"username": {"taken"}, "password": {"correct horse battery"}}, nil)
	mustContain(t, body, "That username is taken")
	_, body = a.post("/panorama/editor/account/username", url.Values{"username": {"editor"}, "password": {"correct horse battery"}}, nil)
	mustContain(t, body, "reserved")
	_, body = a.post("/panorama/editor/account/username", url.Values{"username": {"bennyc"}, "password": {"wrong"}}, nil)
	mustContain(t, body, "Current password is incorrect")
	if resp, _ := a.post("/panorama/editor/account/username", url.Values{"username": {"bennyc"}, "password": {"correct horse battery"}}, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("rename: %d", resp.StatusCode)
	}

	// Password change signs out other sessions but keeps this one.
	other := newClientFor(a)
	other.login("bennyc", "correct horse battery")
	resp, _ := a.post("/panorama/editor/account/password", url.Values{
		"old_password": {"correct horse battery"}, "new_password": {"a much better password"},
	}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("password change: %d", resp.StatusCode)
	}
	if resp, _ := a.get("/panorama/editor"); resp.StatusCode != 200 {
		t.Errorf("current session lost after password change: %d", resp.StatusCode)
	}
	if resp, _ := other.get("/panorama/editor"); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("other session survived password change: %d", resp.StatusCode)
	}
	a.post("/logout", url.Values{}, nil)
	a.login("bennyc", "a much better password")
}

// newClientFor returns a second browser (separate cookies) on the same app.
func newClientFor(a *testApp) *testApp {
	b := *a
	jar, _ := newJar()
	b.c = &http.Client{Jar: jar, CheckRedirect: a.c.CheckRedirect}
	return &b
}
