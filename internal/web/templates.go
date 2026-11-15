package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/chenbenny31/plurb/internal/store"
)

//go:embed templates static
var assets embed.FS

// view is the data every page template receives.
type view struct {
	Title      string
	User       *store.User
	Panorama   bool   // nav variant: inside Panorama vs the plurb landing
	Q          string // current search query, echoed in the nav search box
	OG         *ogMeta
	RSSURL     string
	OpenSignup bool
	GitHub     bool
	Data       any
}

type ogMeta struct {
	Title, Description, Image, URL string
}

// templates maps page name ("login") to a parsed set: layout + page.
type templates map[string]*template.Template

func loadTemplates(funcs template.FuncMap) (templates, error) {
	pages, err := fs.Glob(assets, "templates/pages/*.html")
	if err != nil {
		return nil, err
	}
	t := templates{}
	for _, p := range pages {
		name := strings.TrimSuffix(strings.TrimPrefix(p, "templates/pages/"), ".html")
		tmpl, err := template.New(name).Funcs(funcs).ParseFS(assets, "templates/layout.html", p)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", name, err)
		}
		t[name] = tmpl
	}
	return t, nil
}

// render executes into a buffer first so a template error never leaves a
// half-written page with a 200 status.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, v *view) {
	tmpl, ok := s.tmpl[page]
	if !ok {
		s.serverError(w, r, fmt.Errorf("no template %q", page))
		return
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", v); err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// newView fills the fields every page needs.
func (s *Server) newView(r *http.Request, title string, data any) *view {
	return &view{
		Title:      title,
		User:       currentUser(r),
		Panorama:   true,
		OpenSignup: s.cfg.OpenSignup,
		GitHub:     s.gh != nil,
		Data:       data,
	}
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, "error", s.newView(r, "Not found",
		errorPage{Code: 404, Message: "There's nothing here."}))
}

func (s *Server) badRequest(w http.ResponseWriter, r *http.Request, msg string) {
	s.render(w, r, http.StatusBadRequest, "error", s.newView(r, "Bad request",
		errorPage{Code: 400, Message: msg}))
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("server error", "path", r.URL.Path, "err", err)
	// Plain text: the error page itself might be what failed.
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

type errorPage struct {
	Code    int
	Message string
}

func templateFuncs(assetURL func(string) string) template.FuncMap {
	return template.FuncMap{
		"asset": assetURL,
		"date": func(t any) string {
			switch v := t.(type) {
			case time.Time:
				return v.Format("2006-01-02")
			case *time.Time:
				if v != nil {
					return v.Format("2006-01-02")
				}
			}
			return ""
		},
		"add": func(a, b int) int { return a + b },
	}
}

// assetVersions maps static paths to a short content hash used as a cache
// buster, so static files can be cached forever.
func assetVersions() (map[string]string, error) {
	v := map[string]string{}
	err := fs.WalkDir(assets, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := assets.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		v[strings.TrimPrefix(p, "static/")] = hex.EncodeToString(sum[:4])
		return nil
	})
	return v, err
}
