package documents

import (
	"strings"
	"testing"
)

// renderString renders src and returns the page as a string.
func renderString(t *testing.T, src, title string) string {
	t.Helper()
	out, err := render([]byte(src), title)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestRenderGFM checks tables, task lists and strikethrough.
func TestRenderGFM(t *testing.T) {
	got := renderString(t, "|a|b|\n|-|-|\n|1|2|\n\n- [x] done\n\n~~gone~~\n", "t")
	for _, want := range []string{"<table>", `<input checked="" disabled="" type="checkbox"`, "<del>"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
}

// TestRenderHeadingIDs checks headings get slug IDs.
func TestRenderHeadingIDs(t *testing.T) {
	if got := renderString(t, "## Global Constraints\n", "t"); !strings.Contains(got, `id="global-constraints"`) {
		t.Errorf("no heading id in %s", got)
	}
}

// TestRenderHighlights checks code uses chroma classes, not inline styles.
func TestRenderHighlights(t *testing.T) {
	got := renderString(t, "```go\nfunc main() {}\n```\n", "t")
	if !strings.Contains(got, `class="chroma"`) {
		t.Errorf("no chroma class in %s", got)
	}
	body := got[strings.Index(got, "</style>"):]
	if strings.Contains(body, "style=") {
		t.Errorf("inline style in body: %s", body)
	}
}

// TestRenderKeepsRawHTMLWithoutScripts checks raw HTML stays and the page adds no script.
func TestRenderKeepsRawHTMLWithoutScripts(t *testing.T) {
	got := renderString(t, "<details><summary>x</summary>y</details>\n", "t")
	if !strings.Contains(got, "<details><summary>x</summary>y</details>") {
		t.Errorf("raw html dropped: %s", got)
	}
	if strings.Contains(got, "<script") {
		t.Errorf("page has a script: %s", got)
	}
}

// TestRenderPage checks the title, stylesheet, themes and home link.
func TestRenderPage(t *testing.T) {
	got := renderString(t, "# Hi\n", "a <b>.md")
	for _, want := range []string{"<title>a &lt;b&gt;.md</title>", "<style>", "prefers-color-scheme: dark", `href="/">Nearhost</a>`, `name="viewport"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if !strings.Contains(got, "prefers-color-scheme: light") && !strings.Contains(got, ":root {") {
		t.Errorf("no light theme")
	}
}

// TestRenderRelativeLinksUntouched checks relative links keep their href.
func TestRenderRelativeLinksUntouched(t *testing.T) {
	if got := renderString(t, "[plan](../plans/p.md)\n", "t"); !strings.Contains(got, `href="../plans/p.md"`) {
		t.Errorf("link changed: %s", got)
	}
}
