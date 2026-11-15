package web

import (
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/chenbenny31/plurb/internal/markdown"
	"github.com/chenbenny31/plurb/internal/store"
)

const pageSize = 10

// ---- Panorama index ----

type indexPage struct {
	Posts     []*store.Post
	Popular   []store.PostRef
	Tags      []store.Tag
	Series    []store.Series
	ActiveTag string
	Page      int
	Pages     []int
	PrevPage  int // 0 = none
	NextPage  int // 0 = none
}

func (s *Server) panoramaIndex(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tag := r.URL.Query().Get("tag")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	posts, total, err := s.store.ListPublished(ctx, tag, pageSize, (page-1)*pageSize)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	tags, err := s.store.TagsInUse(ctx, 40)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	series, err := s.store.SeriesWithPosts(ctx, 0)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	popular, err := s.popularPosts(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	d := indexPage{Posts: posts, Popular: popular, Tags: tags, Series: series, ActiveTag: tag, Page: page}
	last := (total + pageSize - 1) / pageSize
	for i := 1; i <= last; i++ {
		d.Pages = append(d.Pages, i)
	}
	if page > 1 {
		d.PrevPage = page - 1
	}
	if page < last {
		d.NextPage = page + 1
	}
	v := s.newView(r, "Panorama", d)
	v.RSSURL = "/panorama/feed.xml"
	s.render(w, r, http.StatusOK, "index", v)
}

// ---- search ----

type searchPage struct {
	Q       string
	Results []*store.Post
	Ran     bool
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 200 {
		q = q[:200]
	}
	d := searchPage{Q: q}
	if q != "" {
		res, err := s.store.Search(r.Context(), q, 50)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		d.Results, d.Ran = res, true
	}
	v := s.newView(r, "Search", d)
	v.Q = q
	s.render(w, r, http.StatusOK, "search", v)
}

// ---- author pages ----

type profilePage struct {
	Author *store.User
	Tab    string // "about" or "posts"
	About  template.HTML
	Posts  []*store.Post
	Series []store.Series
}

func (s *Server) author(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	u, err := s.store.UserByUsername(r.Context(), r.PathValue("username"))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return nil, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	return u, true
}

func (s *Server) profile(w http.ResponseWriter, r *http.Request) {
	u, ok := s.author(w, r)
	if !ok {
		return
	}
	d := profilePage{Author: u, Tab: "posts"}
	if r.URL.Query().Get("tab") == "about" || strings.HasSuffix(r.URL.Path, "/about") {
		d.Tab = "about"
	}
	if d.Tab == "about" {
		out, err := s.md.Render(u.AboutMD)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		d.About = out.HTML
	} else {
		var err error
		if d.Posts, err = s.store.PublishedByAuthor(r.Context(), u.ID, ""); err != nil {
			s.serverError(w, r, err)
			return
		}
		if d.Series, err = s.store.SeriesWithPosts(r.Context(), u.ID); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	v := s.newView(r, u.DisplayName, d)
	v.RSSURL = "/panorama/" + u.Username + "/feed.xml"
	s.render(w, r, http.StatusOK, "profile", v)
}

type listPage struct {
	Author  *store.User
	Heading string
	Note    string
	Posts   []*store.Post
}

func (s *Server) authorTag(w http.ResponseWriter, r *http.Request) {
	u, ok := s.author(w, r)
	if !ok {
		return
	}
	tag, err := s.store.TagBySlug(r.Context(), r.PathValue("tag"))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	posts, err := s.store.PublishedByAuthor(r.Context(), u.ID, tag.Slug)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "list", s.newView(r, "#"+tag.Name,
		listPage{Author: u, Heading: "Posts tagged " + tag.Name, Posts: posts}))
}

func (s *Server) authorSeries(w http.ResponseWriter, r *http.Request) {
	u, ok := s.author(w, r)
	if !ok {
		return
	}
	se, err := s.store.SeriesBySlug(r.Context(), u.ID, r.PathValue("series"))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	posts, err := s.store.SeriesPosts(r.Context(), se.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "list", s.newView(r, se.Title,
		listPage{Author: u, Heading: se.Title, Note: se.Description, Posts: posts}))
}

// ---- single post ----

type postPage struct {
	Post         *store.Post
	Body         template.HTML
	TOC          []markdown.TOCEntry
	Minutes      int
	Older, Newer *store.Post
	Related      []*store.Post
	Series       *store.Series
	SeriesPosts  []*store.Post
	Preview      bool // draft shown to its author
}

func (s *Server) post(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := s.author(w, r)
	if !ok {
		return
	}
	p, err := s.store.PostBySlug(ctx, u.ID, r.PathValue("slug"))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	viewer := currentUser(r)
	isOwner := viewer != nil && viewer.ID == p.AuthorID
	if !p.Published() && !isOwner {
		s.notFound(w, r)
		return
	}

	out, err := s.renderPost(ctx, p)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := postPage{Post: p, Body: out.HTML, TOC: out.TOC, Minutes: out.ReadingMinutes, Preview: !p.Published()}
	if p.Published() {
		if d.Older, d.Newer, err = s.store.Neighbours(ctx, p); err != nil {
			s.serverError(w, r, err)
			return
		}
		if d.Related, err = s.store.Related(ctx, p, 5); err != nil {
			s.serverError(w, r, err)
			return
		}
		if !isOwner && !isBot(r.UserAgent()) {
			if err := s.kv.Views.Hit(ctx, p.ID, s.visitor(r)); err != nil {
				s.log.Warn("count view", "err", err)
			}
		}
	}
	if p.SeriesID.Valid {
		if d.Series, err = s.store.SeriesByID(ctx, p.SeriesID.Int64); err != nil {
			s.serverError(w, r, err)
			return
		}
		if d.SeriesPosts, err = s.store.SeriesPosts(ctx, p.SeriesID.Int64); err != nil {
			s.serverError(w, r, err)
			return
		}
	}

	v := s.newView(r, p.Title, d)
	v.OG = &ogMeta{
		Title: p.Title, Description: p.Description, Image: p.CoverURL,
		URL: s.cfg.BaseURL + "/panorama/" + u.Username + "/" + p.Slug,
	}
	v.RSSURL = "/panorama/" + u.Username + "/feed.xml"
	if d.Preview {
		w.Header().Set("X-Robots-Tag", "noindex")
	}
	s.render(w, r, http.StatusOK, "post", v)
}

// isBot skips obvious crawlers so they don't inflate view counts.
func isBot(ua string) bool {
	ua = strings.ToLower(ua)
	return ua == "" || strings.Contains(ua, "bot") || strings.Contains(ua, "crawl") ||
		strings.Contains(ua, "spider") || strings.Contains(ua, "curl")
}
