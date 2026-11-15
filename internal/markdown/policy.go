package markdown

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// policy is an AST transformer that runs after parsing and before rendering.
// It rewrites or neutralises anything that would let a post load or run
// something we didn't intend.
type policy struct{}

func (policy) Transform(doc *ast.Document, reader text.Reader, _ parser.Context) {
	source := reader.Source()

	// Collect first, mutate after: changing the tree mid-walk skips nodes.
	var embeds []*ast.Paragraph
	var images []*ast.Image
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := n.(type) {
		case *ast.Paragraph:
			if embedFor(t, source) != nil {
				embeds = append(embeds, t)
				return ast.WalkSkipChildren, nil
			}
		case *ast.Link:
			if !allowedLink(string(t.Destination)) {
				t.Destination = nil
			}
			t.SetAttributeString("rel", []byte("nofollow ugc noopener"))
		case *ast.AutoLink:
			t.SetAttributeString("rel", []byte("nofollow ugc noopener"))
		case *ast.Image:
			images = append(images, t)
		}
		return ast.WalkContinue, nil
	})

	for _, p := range embeds {
		p.Parent().ReplaceChild(p.Parent(), p, embedFor(p, source))
	}
	for _, img := range images {
		fixImage(img)
	}
}

// allowedLink accepts http(s), mailto and same-site references.
func allowedLink(dest string) bool {
	if dest == "" || strings.HasPrefix(dest, "#") ||
		(strings.HasPrefix(dest, "/") && !strings.HasPrefix(dest, "//")) {
		return true
	}
	u, err := url.Parse(dest)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.Host != ""
	case "mailto":
		return true
	}
	return false
}

// fixImage keeps https images (lazy-loaded, no referrer leaked to the host).
// Anything else becomes a plain link to the same URL if it's http, or is
// replaced by its alt text, so posts can't embed insecure or data: content.
func fixImage(img *ast.Image) {
	u, err := url.Parse(string(img.Destination))
	if err == nil && strings.EqualFold(u.Scheme, "https") && u.Host != "" {
		img.SetAttributeString("loading", []byte("lazy"))
		img.SetAttributeString("referrerpolicy", []byte("no-referrer"))
		return
	}
	link := ast.NewLink()
	if err == nil && strings.EqualFold(u.Scheme, "http") && u.Host != "" {
		link.Destination = img.Destination
		link.SetAttributeString("rel", []byte("nofollow ugc noopener"))
	}
	for c := img.FirstChild(); c != nil; {
		next := c.NextSibling()
		link.AppendChild(link, c)
		c = next
	}
	img.Parent().ReplaceChild(img.Parent(), img, link)
}

// ---- video embeds ----

// KindEmbed is a block that renders as an allowlisted video player.
var KindEmbed = ast.NewNodeKind("Embed")

type embedNode struct {
	ast.BaseBlock
	// src is built by us from a validated id, never copied from the post.
	src   string
	title string
}

func (*embedNode) Kind() ast.NodeKind              { return KindEmbed }
func (n *embedNode) Dump(source []byte, level int) { ast.DumpHelper(n, source, level, nil, nil) }

var (
	youtubeID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	bvid      = regexp.MustCompile(`^BV[0-9A-Za-z]{10}$`)
	vimeoID   = regexp.MustCompile(`^[0-9]{1,12}$`)
)

// embedFor returns a player node when the paragraph is nothing but a bare
// link to a supported video page; otherwise nil.
func embedFor(p *ast.Paragraph, source []byte) *embedNode {
	if p.ChildCount() != 1 {
		return nil
	}
	var raw string
	switch c := p.FirstChild().(type) {
	case *ast.AutoLink:
		raw = string(c.URL(source))
	case *ast.Link:
		// Only "[url](url)" style links; a link with custom text stays a link.
		if plainText(c, source) != string(c.Destination) {
			return nil
		}
		raw = string(c.Destination)
	default:
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	host = strings.TrimPrefix(host, "m.")
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")

	switch host {
	case "youtube.com":
		id := u.Query().Get("v")
		if len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "embed" || parts[0] == "live") {
			id = parts[1]
		}
		if youtubeID.MatchString(id) {
			return &embedNode{src: "https://www.youtube-nocookie.com/embed/" + id, title: "YouTube video"}
		}
	case "youtu.be":
		if len(parts) == 1 && youtubeID.MatchString(parts[0]) {
			return &embedNode{src: "https://www.youtube-nocookie.com/embed/" + parts[0], title: "YouTube video"}
		}
	case "bilibili.com":
		if len(parts) >= 2 && parts[0] == "video" && bvid.MatchString(parts[1]) {
			return &embedNode{src: "https://player.bilibili.com/player.html?bvid=" + parts[1] + "&autoplay=0", title: "Bilibili video"}
		}
	case "vimeo.com":
		if len(parts) == 1 && vimeoID.MatchString(parts[0]) {
			return &embedNode{src: "https://player.vimeo.com/video/" + parts[0], title: "Vimeo video"}
		}
	}
	return nil
}
