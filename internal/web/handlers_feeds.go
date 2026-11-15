package web

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chenbenny31/plurb/internal/store"
)

const feedSize = 20

// ---- RSS 2.0 ----

type rss struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Atom    string     `xml:"xmlns:atom,attr"`
	DC      string     `xml:"xmlns:dc,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	SelfLink    atomLink  `xml:"atom:link"`
	LastBuild   string    `xml:"lastBuildDate,omitempty"`
	Items       []rssItem `xml:"item"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

type rssItem struct {
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	GUID        string   `xml:"guid"`
	PubDate     string   `xml:"pubDate"`
	Author      string   `xml:"dc:creator,omitempty"`
	Categories  []string `xml:"category"`
	Description string   `xml:"description"` // full rendered HTML, XML-escaped
}

func (s *Server) globalFeed(w http.ResponseWriter, r *http.Request) {
	posts, _, err := s.store.ListPublished(r.Context(), "", feedSize, 0)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.writeFeed(w, r, "Panorama — plurb", "/panorama", "/panorama/feed.xml",
		"Latest posts on plurb Panorama", posts)
}

func (s *Server) authorFeed(w http.ResponseWriter, r *http.Request) {
	u, ok := s.author(w, r)
	if !ok {
		return
	}
	posts, err := s.store.PublishedByAuthor(r.Context(), u.ID, "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if len(posts) > feedSize {
		posts = posts[:feedSize]
	}
	base := "/panorama/" + u.Username
	s.writeFeed(w, r, u.DisplayName+" on Panorama", base, base+"/feed.xml",
		"Posts by "+u.DisplayName, posts)
}

func (s *Server) writeFeed(w http.ResponseWriter, r *http.Request, title, link, self, desc string, posts []*store.Post) {
	ch := rssChannel{
		Title: title, Link: s.cfg.BaseURL + link, Description: desc,
		SelfLink: atomLink{Href: s.cfg.BaseURL + self, Rel: "self", Type: "application/rss+xml"},
	}
	for i, p := range posts {
		out, err := s.renderPost(r.Context(), p)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		url := s.cfg.BaseURL + "/panorama/" + p.AuthorUsername + "/" + p.Slug
		item := rssItem{
			Title: p.Title, Link: url, GUID: url,
			PubDate: p.PublishedAt.UTC().Format(time.RFC1123Z),
			Author:  p.AuthorDisplayName, Description: string(out.HTML),
		}
		for _, t := range p.Tags {
			item.Categories = append(item.Categories, t.Name)
		}
		ch.Items = append(ch.Items, item)
		if i == 0 {
			ch.LastBuild = item.PubDate
		}
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=600")
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	doc := rss{Version: "2.0", Atom: "http://www.w3.org/2005/Atom", DC: "http://purl.org/dc/elements/1.1/", Channel: ch}
	if err := enc.Encode(doc); err != nil {
		s.log.Error("encode feed", "err", err)
	}
}

// ---- sitemap & robots ----

type urlset struct {
	XMLName xml.Name     `xml:"urlset"`
	NS      string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

func (s *Server) sitemap(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.SitemapEntries(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	set := urlset{NS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	set.URLs = append(set.URLs, sitemapURL{Loc: s.cfg.BaseURL + "/"}, sitemapURL{Loc: s.cfg.BaseURL + "/panorama"})
	authors := map[string]bool{}
	for _, e := range entries {
		if !authors[e.Username] {
			authors[e.Username] = true
			set.URLs = append(set.URLs, sitemapURL{Loc: s.cfg.BaseURL + "/panorama/" + e.Username})
		}
		set.URLs = append(set.URLs, sitemapURL{
			Loc:     s.cfg.BaseURL + "/panorama/" + e.Username + "/" + e.Slug,
			LastMod: e.UpdatedAt.UTC().Format("2006-01-02"),
		})
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	if err := xml.NewEncoder(w).Encode(set); err != nil {
		s.log.Error("encode sitemap", "err", err)
	}
}

func (s *Server) robots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "User-agent: *\nDisallow: /panorama/editor\nDisallow: /auth/\n\nSitemap: %s/sitemap.xml\n", s.cfg.BaseURL)
}

// ---- export ----

// exportPosts streams a zip of the author's posts (drafts included) as
// markdown files with YAML front matter — your words, portable.
func (s *Server) exportPosts(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	posts, err := s.store.PostsByAuthor(r.Context(), u.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	series, err := s.store.SeriesByAuthor(r.Context(), u.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// series id -> title
	seriesTitle := map[int64]string{}
	for _, se := range series {
		seriesTitle[se.ID] = se.Title
	}

	name := "plurb-" + u.Username + "-" + time.Now().UTC().Format("20060102") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	zw := zip.NewWriter(w)
	for _, p := range posts {
		f, err := zw.CreateHeader(&zip.FileHeader{
			Name: p.Slug + ".md", Method: zip.Deflate, Modified: p.UpdatedAt,
		})
		if err != nil {
			s.log.Error("export", "err", err)
			return
		}
		f.Write([]byte(frontMatter(p, seriesTitle)))
		f.Write([]byte(p.BodyMD))
	}
	if u.AboutMD != "" {
		if f, err := zw.Create("about.md"); err == nil {
			f.Write([]byte(u.AboutMD))
		}
	}
	if err := zw.Close(); err != nil {
		s.log.Error("export", "err", err)
	}
}

func frontMatter(p *store.Post, seriesTitle map[int64]string) string {
	var b strings.Builder
	// strconv.Quote output is valid YAML double-quoted scalar syntax.
	q := strconv.Quote
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %s\nslug: %s\nstatus: %s\n", q(p.Title), p.Slug, p.Status)
	if p.Description != "" {
		fmt.Fprintf(&b, "description: %s\n", q(p.Description))
	}
	if p.CoverURL != "" {
		fmt.Fprintf(&b, "cover: %s\n", q(p.CoverURL))
	}
	if p.PublishedAt != nil {
		fmt.Fprintf(&b, "published: %s\n", p.PublishedAt.UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "updated: %s\n", p.UpdatedAt.UTC().Format(time.RFC3339))
	if len(p.Tags) > 0 {
		names := make([]string, len(p.Tags))
		for i, t := range p.Tags {
			names[i] = q(t.Name)
		}
		fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(names, ", "))
	}
	if p.SeriesID.Valid {
		fmt.Fprintf(&b, "series: %s\n", q(seriesTitle[p.SeriesID.Int64]))
	}
	b.WriteString("---\n\n")
	return b.String()
}
