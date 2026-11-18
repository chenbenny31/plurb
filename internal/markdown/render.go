package markdown

import (
	"bytes"
	"html/template"
	"sync"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

// nodeRenderer renders our embed node and replaces goldmark's code blocks
// with chroma-highlighted ones.
type nodeRenderer struct{}

func (nodeRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(KindEmbed, renderEmbed)
	reg.Register(ast.KindFencedCodeBlock, renderFencedCode)
}

func renderEmbed(w util.BufWriter, _ []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	e := n.(*embedNode)
	w.WriteString(`<div class="embed"><iframe src="`)
	w.WriteString(template.HTMLEscapeString(e.src))
	w.WriteString(`" title="`)
	w.WriteString(template.HTMLEscapeString(e.title))
	w.WriteString(`" loading="lazy" referrerpolicy="strict-origin-when-cross-origin" ` +
		`sandbox="allow-scripts allow-same-origin allow-presentation allow-popups" ` +
		`allow="fullscreen; picture-in-picture"></iframe></div>` + "\n")
	return ast.WalkSkipChildren, nil
}

// Classes, not inline styles: the CSP forbids inline style attributes.
var codeFormatter = chromahtml.New(chromahtml.WithClasses(true))

func renderFencedCode(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	b := n.(*ast.FencedCodeBlock)
	var code bytes.Buffer
	lines := b.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		code.Write(seg.Value(source))
	}

	lexer := lexers.Get(string(b.Language(source)))
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)
	it, err := lexer.Tokenise(nil, code.String())
	if err == nil {
		err = codeFormatter.Format(w, styles.Fallback, it)
	}
	if err != nil {
		// Highlighting is cosmetic; fall back to escaped plain text.
		w.WriteString(`<pre class="chroma"><code>`)
		w.WriteString(template.HTMLEscapeString(code.String()))
		w.WriteString("</code></pre>\n")
	}
	return ast.WalkSkipChildren, nil
}

var (
	codeCSSOnce sync.Once
	codeCSS     string
)

// CodeCSS returns the stylesheet for highlighted code (GitHub's light theme;
// the site is white-only).
func CodeCSS() string {
	codeCSSOnce.Do(func() {
		var light bytes.Buffer
		codeFormatter.WriteCSS(&light, styles.Get("github"))
		codeCSS = light.String()
	})
	return codeCSS
}
