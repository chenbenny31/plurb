package markdown

import (
	"strings"
	"testing"
)

func render(t *testing.T, src string) Rendered {
	t.Helper()
	out, err := New().Render(src)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestUnsafeContentIsNeutralised(t *testing.T) {
	cases := []struct {
		name, src string
		banned    []string
	}{
		{"raw script", "<script>alert(1)</script>", []string{"<script"}},
		{"raw iframe", `<iframe src="https://evil.example"></iframe>`, []string{"<iframe"}},
		{"inline handler", `<img src=x onerror=alert(1)>`, []string{"onerror"}},
		{"javascript link", "[click](javascript:alert(1))", []string{"javascript:"}},
		{"data link", "[x](data:text/html;base64,PHNjcmlwdD4=)", []string{"data:"}},
		{"data image", "![x](data:image/png;base64,AAAA)", []string{"<img", "data:"}},
		{"http image", "![cat](http://example.com/cat.png)", []string{"<img"}},
		{"bare non-video link", "https://evil.example/watch?v=dQw4w9WgXcQ", []string{"<iframe"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			html := string(render(t, c.src).HTML)
			for _, b := range c.banned {
				if strings.Contains(html, b) {
					t.Errorf("output contains %q:\n%s", b, html)
				}
			}
		})
	}
}

func TestExternalMedia(t *testing.T) {
	html := string(render(t, "![cat](https://example.com/cat.png)").HTML)
	for _, want := range []string{`src="https://example.com/cat.png"`, `loading="lazy"`, `referrerpolicy="no-referrer"`} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %s in %s", want, html)
		}
	}

	embeds := map[string]string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":  "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ":                 "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ",
		"https://www.bilibili.com/video/BV1GJ411x7h7/": "https://player.bilibili.com/player.html?bvid=BV1GJ411x7h7&amp;autoplay=0",
		"<https://vimeo.com/76979871>":                 "https://player.vimeo.com/video/76979871",
	}
	for src, want := range embeds {
		html := string(render(t, src).HTML)
		if !strings.Contains(html, `<iframe src="`+want+`"`) {
			t.Errorf("%s: want iframe %s, got %s", src, want, html)
		}
	}

	// A video link inside a sentence stays a link.
	html = string(render(t, "watch https://youtu.be/dQw4w9WgXcQ now").HTML)
	if strings.Contains(html, "<iframe") || !strings.Contains(html, `rel="nofollow ugc noopener"`) {
		t.Errorf("inline video link: %s", html)
	}
}

func TestTOCAndHeadingIDs(t *testing.T) {
	out := render(t, "# Title\n\n## Why \"Go\"?\n\n### 并发模型\n\n## Why \"Go\"?\n")
	want := []TOCEntry{
		{2, "Why “Go”?", "why-go"},
		{3, "并发模型", "并发模型"},
		{2, "Why “Go”?", "why-go-2"},
	}
	if len(out.TOC) != len(want) {
		t.Fatalf("toc = %+v", out.TOC)
	}
	for i := range want {
		if out.TOC[i] != want[i] {
			t.Errorf("toc[%d] = %+v, want %+v", i, out.TOC[i], want[i])
		}
	}
	if !strings.Contains(string(out.HTML), `<h3 id="并发模型">`) {
		t.Errorf("heading id missing: %s", out.HTML)
	}
}

func TestCodeHighlightUsesClasses(t *testing.T) {
	html := string(render(t, "```go\nfunc main() {}\n```").HTML)
	if !strings.Contains(html, `class="chroma"`) || strings.Contains(html, "style=") {
		t.Errorf("code block: %s", html)
	}
	if !strings.Contains(CodeCSS(), "html.light .chroma") {
		t.Error("light theme css missing")
	}
}

func TestExtensions(t *testing.T) {
	html := string(render(t, "a -- b... \"q\"\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n- [x] done\n\nnote[^1]\n\n[^1]: foot").HTML)
	for _, want := range []string{"&ndash;", "&hellip;", "&ldquo;", "<table>", `type="checkbox"`, `class="footnotes"`} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %s in %s", want, html)
		}
	}
}

func TestReadingStats(t *testing.T) {
	words, minutes := readingStats(strings.Repeat("word ", 400))
	if words != 400 || minutes != 2 {
		t.Errorf("english: %d words, %d min", words, minutes)
	}
	words, minutes = readingStats(strings.Repeat("并发", 400))
	if words != 800 || minutes != 2 {
		t.Errorf("cjk: %d words, %d min", words, minutes)
	}
}

func TestSlugify(t *testing.T) {
	if got := Slugify("Hello, World! Go 1.24", false); got != "hello-world-go-1-24" {
		t.Errorf("got %q", got)
	}
	if got := Slugify("并发 Go", false); got != "go" {
		t.Errorf("ascii-only slug: got %q", got)
	}
}
