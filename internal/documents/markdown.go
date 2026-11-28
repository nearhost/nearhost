package documents

import (
	"bytes"
	_ "embed"
	"html"

	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	gmhtml "github.com/yuin/goldmark/renderer/html"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
)

// markdownCSS is the stylesheet inlined into every rendered page. The
// markdown CSP allows no external styles.
//
//go:embed markdown.css
var markdownCSS string

// converter turns GitHub-flavoured markdown into HTML. Raw HTML is kept
// because the page's CSP already blocks scripts. Code is highlighted with
// classes, which markdownCSS styles for both themes.
var converter = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM,
		highlighting.NewHighlighting(highlighting.WithFormatOptions(chromahtml.WithClasses(true))),
	),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(gmhtml.WithUnsafe()),
)

func init() { renderMarkdown = render }

// render turns markdown source into a complete HTML page titled title, with
// its stylesheet inlined and a link back to Nearhost's home page. With th,
// th's stylesheet replaces markdownCSS, <html> carries th's attributes and
// there is no link home.
func render(src []byte, title string, th *Theme) ([]byte, error) {
	var body bytes.Buffer
	if err := converter.Convert(src, &body); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	css := []byte(markdownCSS)
	out.WriteString("<!doctype html>\n<html lang=\"en\"")
	if th != nil {
		out.WriteString(th.attrs())
		css = th.CSS
	}
	out.WriteString(">\n<head>\n<meta charset=\"utf-8\">\n" +
		"<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n" +
		"<meta name=\"color-scheme\" content=\"light dark\">\n<title>")
	out.WriteString(html.EscapeString(title))
	out.WriteString("</title>\n<style>\n")
	out.Write(css)
	out.WriteString("</style>\n</head>\n<body>\n")
	if th == nil {
		// A themed page is read in the web app's frame, where / would load
		// the app inside it, which the app's policy refuses.
		out.WriteString("<a class=\"home\" href=\"/\">Nearhost</a>\n")
	}
	out.WriteString("<article>\n")
	out.Write(body.Bytes())
	out.WriteString("</article>\n</body>\n</html>\n")
	return out.Bytes(), nil
}
