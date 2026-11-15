package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/chenbenny31/plurb/internal/auth"
	"github.com/chenbenny31/plurb/internal/cache"
	"github.com/chenbenny31/plurb/internal/config"
	"github.com/chenbenny31/plurb/internal/db/dbtest"
	"github.com/chenbenny31/plurb/internal/notify"
)

// testApp is a running server plus a cookie-keeping client that doesn't
// follow redirects, so tests can assert on them.
type testApp struct {
	t     *testing.T
	srv   *Server
	ts    *httptest.Server
	c     *http.Client
	dbURL string
}

func newTestApp(t *testing.T, mutate func(*config.Config)) *testApp {
	t.Helper()
	pool, dbURL := dbtest.NewWithURL(t)
	cfg := config.Config{BaseURL: "http://localhost", Dev: true}
	if mutate != nil {
		mutate(&cfg)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := NewServer(cfg, log, pool, cache.NewMemory(), notify.NewHub(log))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return &testApp{t: t, srv: srv, ts: ts, c: c, dbURL: dbURL}
}

func (a *testApp) get(path string) (*http.Response, string) {
	a.t.Helper()
	return a.do("GET", path, nil, nil)
}

func (a *testApp) post(path string, form url.Values, hdr map[string]string) (*http.Response, string) {
	a.t.Helper()
	return a.do("POST", path, form, hdr)
}

func (a *testApp) do(method, path string, form url.Values, hdr map[string]string) (*http.Response, string) {
	a.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, a.ts.URL+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := a.c.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func (a *testApp) createUser(username, password string) {
	a.t.Helper()
	hash, err := auth.Hash(password)
	if err != nil {
		a.t.Fatal(err)
	}
	if _, err := a.srv.store.CreateUser(context.Background(), username, username, hash, "member"); err != nil {
		a.t.Fatal(err)
	}
}

func (a *testApp) login(username, password string) {
	a.t.Helper()
	resp, _ := a.post("/login", url.Values{"username": {username}, "password": {password}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		a.t.Fatalf("login %s: status %d", username, resp.StatusCode)
	}
}

func TestHealthzAndSecurityHeaders(t *testing.T) {
	a := newTestApp(t, nil)
	resp, body := a.get("/healthz")
	if resp.StatusCode != 200 || body != "ok\n" {
		t.Fatalf("healthz: %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("missing Content-Security-Policy")
	}
	if resp, _ := a.get("/static/css/main.css"); resp.StatusCode != 200 {
		t.Fatalf("static css: %d", resp.StatusCode)
	}
	if resp, _ := a.get("/static/css/"); resp.StatusCode != 404 {
		t.Fatalf("directory listing: %d", resp.StatusCode)
	}
}

func TestLoginLogout(t *testing.T) {
	a := newTestApp(t, nil)
	a.createUser("benny", "correct horse battery")

	resp, body := a.post("/login", url.Values{"username": {"benny"}, "password": {"wrong password!"}}, nil)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "Invalid username or password") {
		t.Fatalf("bad password: %d", resp.StatusCode)
	}

	a.login("benny", "correct horse battery")
	if _, body := a.get("/"); !strings.Contains(body, "logout") {
		t.Fatal("signed-in nav not shown")
	}

	resp, _ = a.post("/logout", url.Values{}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if _, body := a.get("/"); strings.Contains(body, "logout") {
		t.Fatal("still signed in after logout")
	}
}

func TestLoginRejectsOpenRedirect(t *testing.T) {
	a := newTestApp(t, nil)
	a.createUser("benny", "correct horse battery")
	resp, _ := a.post("/login", url.Values{
		"username": {"benny"}, "password": {"correct horse battery"}, "next": {"//evil.example/x"},
	}, nil)
	if loc := resp.Header.Get("Location"); loc != afterLogin {
		t.Fatalf("redirected to %q", loc)
	}
}

func TestCrossOriginPostsBlocked(t *testing.T) {
	a := newTestApp(t, nil)
	form := url.Values{"username": {"x"}, "password": {"y"}}
	if resp, _ := a.post("/login", form, map[string]string{"Sec-Fetch-Site": "cross-site"}); resp.StatusCode != 403 {
		t.Errorf("Sec-Fetch-Site cross-site: %d", resp.StatusCode)
	}
	if resp, _ := a.post("/login", form, map[string]string{"Origin": "https://evil.example"}); resp.StatusCode != 403 {
		t.Errorf("foreign Origin: %d", resp.StatusCode)
	}
	if resp, _ := a.post("/login", form, map[string]string{"Origin": a.ts.URL}); resp.StatusCode == 403 {
		t.Errorf("same Origin blocked")
	}
}

func TestSignup(t *testing.T) {
	closed := newTestApp(t, nil)
	if resp, _ := closed.get("/signup"); resp.StatusCode != 404 {
		t.Fatalf("closed signup page: %d", resp.StatusCode)
	}

	a := newTestApp(t, func(c *config.Config) { c.OpenSignup = true })
	form := url.Values{"username": {"Ada_L"}, "password": {"analytical engine"}, "password_confirm": {"analytical engine"}}
	resp, _ := a.post("/signup", form, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("signup: %d", resp.StatusCode)
	}
	resp, body := a.post("/signup", form, nil)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "taken") {
		t.Fatalf("duplicate signup: %d", resp.StatusCode)
	}
}

// fakeGitHub serves the token and user endpoints for one known code.
func fakeGitHub(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("code") != "good-code" || r.FormValue("client_secret") != "secret" {
			w.Write([]byte(`{"error":"bad_verification_code"}`))
			return
		}
		w.Write([]byte(`{"access_token":"tok123","token_type":"bearer"}`))
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok123" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"id":4242,"login":"Octo-Cat","name":"The Octocat"}`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func (a *testApp) githubLogin(gh *httptest.Server) (*http.Response, string) {
	a.t.Helper()
	a.srv.gh.AuthURL, a.srv.gh.TokenURL, a.srv.gh.UserURL = gh.URL+"/authorize", gh.URL+"/token", gh.URL+"/user"
	resp, _ := a.get("/auth/github")
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound {
		a.t.Fatalf("start: %d %v", resp.StatusCode, err)
	}
	state := loc.Query().Get("state")
	return a.get("/auth/github/callback?code=good-code&state=" + url.QueryEscape(state))
}

func TestGitHubOAuth(t *testing.T) {
	gh := fakeGitHub(t)
	withGitHub := func(open bool) func(*config.Config) {
		return func(c *config.Config) {
			c.GitHubClientID, c.GitHubClientSecret, c.OpenSignup = "id", "secret", open
		}
	}

	t.Run("forged state rejected", func(t *testing.T) {
		a := newTestApp(t, withGitHub(true))
		a.get("/auth/github")
		if resp, _ := a.get("/auth/github/callback?code=good-code&state=forged"); resp.StatusCode != 400 {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})

	t.Run("closed signup refuses new accounts", func(t *testing.T) {
		a := newTestApp(t, withGitHub(false))
		if resp, _ := a.githubLogin(gh); resp.StatusCode != 400 {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})

	t.Run("open signup creates then reuses the account", func(t *testing.T) {
		a := newTestApp(t, withGitHub(true))
		if resp, _ := a.githubLogin(gh); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("first login: %d", resp.StatusCode)
		}
		u, err := a.srv.store.UserByUsername(context.Background(), "octo_cat")
		if err != nil || u.DisplayName != "The Octocat" || u.PasswordHash != "" {
			t.Fatalf("created user: %+v %v", u, err)
		}
		a.post("/logout", url.Values{}, nil)
		a.githubLogin(gh)
		if _, body := a.get("/"); !strings.Contains(body, "logout") {
			t.Fatal("second github login didn't sign in")
		}
		var n int
		a.srv.db.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
		if n != 1 {
			t.Fatalf("%d users after two logins, want 1", n)
		}
	})

	t.Run("signed-in user links github", func(t *testing.T) {
		a := newTestApp(t, withGitHub(false))
		a.createUser("benny", "correct horse battery")
		a.login("benny", "correct horse battery")
		if resp, _ := a.githubLogin(gh); resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("link: %d", resp.StatusCode)
		}
		u, err := a.srv.store.UserByIdentity(context.Background(), "github", "4242")
		if err != nil || u.Username != "benny" {
			t.Fatalf("identity -> %+v %v", u, err)
		}
	})
}
