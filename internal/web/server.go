// Package web holds the HTTP layer: routing, middleware, handlers and templates.
package web

import (
	"context"
	"database/sql"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/chenbenny31/plurb/internal/auth"
	"github.com/chenbenny31/plurb/internal/config"
	"github.com/chenbenny31/plurb/internal/markdown"
	"github.com/chenbenny31/plurb/internal/store"
)

type Server struct {
	cfg   config.Config
	log   *slog.Logger
	db    *sql.DB
	store *store.Store
	md    *markdown.Renderer
	gh    *auth.GitHub // nil when GitHub login isn't configured
	tmpl  templates
	// static path ("css/main.css") -> content hash for cache busting
	assetVer map[string]string
	views    viewCounter
}

func NewServer(cfg config.Config, log *slog.Logger, db *sql.DB) (*Server, error) {
	s := &Server{cfg: cfg, log: log, db: db, store: store.New(db), md: markdown.New()}
	if cfg.GitHubEnabled() {
		s.gh = auth.NewGitHub(cfg.GitHubClientID, cfg.GitHubClientSecret, cfg.BaseURL+"/auth/github/callback")
	}
	var err error
	if s.assetVer, err = assetVersions(); err != nil {
		return nil, err
	}
	if s.tmpl, err = loadTemplates(templateFuncs(s.assetURL)); err != nil {
		return nil, err
	}
	return s, nil
}

// Handler returns the fully wrapped root handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)

	var h http.Handler = mux
	h = s.loadUser(h)
	h = crossOriginGuard(h)
	h = securityHeaders(h)
	h = s.recoverPanics(h)
	h = s.logRequests(h)
	return h
}

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /static/css/code.css", s.codeCSS)
	mux.Handle("GET /static/", s.staticFiles())

	mux.HandleFunc("GET /{$}", s.landing)

	ed := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, s.requireUser(h)) }
	ed("GET /panorama/editor", s.dashboard)
	ed("GET /panorama/editor/new", s.newPostPage)
	ed("POST /panorama/editor/new", s.createPost)
	ed("POST /panorama/editor/preview", s.previewMarkdown)
	ed("GET /panorama/editor/about", s.aboutPage)
	ed("POST /panorama/editor/about", s.saveAbout)
	ed("GET /panorama/editor/account", s.accountPage)
	ed("POST /panorama/editor/account/username", s.changeUsername)
	ed("POST /panorama/editor/account/password", s.changePassword)
	ed("GET /panorama/editor/posts/{id}", s.editPostPage)
	ed("POST /panorama/editor/posts/{id}", s.updatePost)
	ed("POST /panorama/editor/posts/{id}/publish", s.togglePublish)
	ed("POST /panorama/editor/posts/{id}/delete", s.deletePost)

	mux.HandleFunc("GET /panorama", s.panoramaIndex)
	mux.HandleFunc("GET /panorama/search", s.search)
	mux.HandleFunc("GET /panorama/{username}", s.profile)
	mux.HandleFunc("GET /panorama/{username}/about", s.profile)
	mux.HandleFunc("GET /panorama/{username}/tag/{tag}", s.authorTag)
	mux.HandleFunc("GET /panorama/{username}/series/{series}", s.authorSeries)
	mux.HandleFunc("GET /panorama/{username}/{slug}", s.post)

	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /signup", s.signupPage)
	mux.HandleFunc("POST /signup", s.signup)
	mux.HandleFunc("GET /auth/github", s.githubStart)
	mux.HandleFunc("GET /auth/github/callback", s.githubCallback)

	mux.HandleFunc("/", s.notFound)
}

// healthz reports whether the process is up and the database answers.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		s.log.Error("healthz: db ping", "err", err)
		http.Error(w, "db unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok\n"))
}

func (s *Server) landing(w http.ResponseWriter, r *http.Request) {
	v := s.newView(r, "plurb", nil)
	v.Panorama = false
	s.render(w, r, http.StatusOK, "landing", v)
}

// assetURL turns "css/main.css" into "/static/css/main.css?v=<hash>".
func (s *Server) assetURL(path string) string {
	if v, ok := s.assetVer[path]; ok {
		return "/static/" + path + "?v=" + v
	}
	return "/static/" + path
}

func (s *Server) staticFiles() http.Handler {
	sub, _ := fs.Sub(assets, "static")
	files := http.StripPrefix("/static/", http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			s.notFound(w, r) // no directory listings
			return
		}
		if r.URL.Query().Has("v") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		files.ServeHTTP(w, r)
	})
}

func (s *Server) codeCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write([]byte(markdown.CodeCSS()))
}
