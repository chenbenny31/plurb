// Package markdown renders user-written markdown into safe HTML.
//
// Safety model: raw HTML in the source is never passed through (goldmark
// escapes it unless told otherwise), and an AST pass enforces a URL policy
// before rendering — links must be http(s), mailto or relative; images must
// be https. Video is only embedded from an allowlist of players, built from
// a parsed video id, never from the user's URL string.
package markdown

import (
	"bytes"
	"html"
	"html/template"
	"strconv"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// TOCEntry is one h2/h3 heading, linkable via its id.
type TOCEntry struct {
	Level int
	Text  string
	ID    string
}

// Rendered is safe HTML plus reading aids derived from the source.
type Rendered struct {
	HTML           template.HTML
	TOC            []TOCEntry
	Words          int
	ReadingMinutes int
}

type Renderer struct {
	md goldmark.Markdown
}

func New() *Renderer {
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.Table,
			extension.Strikethrough,
			extension.TaskList,
			extension.Footnote,
			extension.Typographer,
			extension.NewLinkify(
				extension.WithLinkifyAllowedProtocols([]string{"http:", "https:"}),
			),
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
			parser.WithASTTransformers(util.Prioritized(policy{}, 100)),
		),
		// Priority 100 beats the default HTML renderer (1000), so our
		// code-block renderer replaces the stock one.
		goldmark.WithRendererOptions(
			renderer.WithNodeRenderers(util.Prioritized(nodeRenderer{}, 100)),
		),
	)
	return &Renderer{md: md}
}

// Render converts markdown to HTML. It never returns raw user HTML.
func (r *Renderer) Render(src string) (Rendered, error) {
	source := []byte(src)
	ctx := parser.NewContext(parser.WithIDs(newHeadingIDs()))
	doc := r.md.Parser().Parse(text.NewReader(source), parser.WithContext(ctx))

	var buf bytes.Buffer
	if err := r.md.Renderer().Render(&buf, source, doc); err != nil {
		return Rendered{}, err
	}
	words, minutes := readingStats(src)
	return Rendered{
		HTML:           template.HTML(buf.String()),
		TOC:            collectTOC(doc, source),
		Words:          words,
		ReadingMinutes: minutes,
	}, nil
}

func collectTOC(doc ast.Node, source []byte) []TOCEntry {
	var toc []TOCEntry
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		h, ok := n.(*ast.Heading)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		if h.Level == 2 || h.Level == 3 {
			id, _ := h.AttributeString("id")
			idBytes, _ := id.([]byte)
			toc = append(toc, TOCEntry{Level: h.Level, Text: plainText(h, source), ID: string(idBytes)})
		}
		return ast.WalkSkipChildren, nil
	})
	return toc
}

// plainText flattens a node's inline children into readable text.
func plainText(n ast.Node, source []byte) string {
	var b strings.Builder
	ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := c.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(source))
		case *ast.String:
			// Typographer emits entities like "&ldquo;"; decode them.
			b.WriteString(html.UnescapeString(string(t.Value)))
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(b.String())
}

// headingIDs implements parser.IDs. Unlike goldmark's default it keeps
// non-ASCII letters, so "## 并发模型" gets id "并发模型" rather than "heading".
type headingIDs struct {
	used map[string]bool
}

func newHeadingIDs() *headingIDs { return &headingIDs{used: map[string]bool{}} }

func (h *headingIDs) Generate(value []byte, _ ast.NodeKind) []byte {
	base := Slugify(html.UnescapeString(string(value)), true)
	if base == "" {
		base = "section"
	}
	id := base
	for i := 2; h.used[id]; i++ {
		id = base + "-" + strconv.Itoa(i)
	}
	h.used[id] = true
	return []byte(id)
}

func (h *headingIDs) Put(value []byte) { h.used[string(value)] = true }

// Slugify lowercases s and joins runs of letters/digits with '-'.
// With unicode=false only a-z and 0-9 survive (used for URL slugs).
func Slugify(s string, unicodeLetters bool) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		keep := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if unicodeLetters && !keep {
			keep = unicode.IsLetter(r) || unicode.IsDigit(r)
		}
		if keep {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	return b.String()
}

// readingStats counts latin words and CJK characters separately: CJK text
// has no spaces, so each character counts as one unit, read about twice as
// fast as an English word.
func readingStats(src string) (words, minutes int) {
	latin, cjk := 0, 0
	inWord := false
	for _, r := range src {
		switch {
		case unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r):
			cjk++
			inWord = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if !inWord {
				latin++
				inWord = true
			}
		default:
			inWord = false
		}
	}
	words = latin + cjk
	// 200 English words or 400 CJK characters per minute.
	minutes = (latin*2 + cjk + 399) / 400
	if minutes < 1 {
		minutes = 1
	}
	return words, minutes
}
