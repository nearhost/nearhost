package documents

import (
	"bytes"
	"context"

	"golang.org/x/net/html"
)

// themedHTMLCSP is the policy an HTML document is sent with when it is
// themed: today's sandbox without scripts, and no script from anywhere. The
// web app frames themed documents without scripts, so none should run, and
// the inlined stylesheet needs nothing new: the policy limits no styles.
const themedHTMLCSP = "sandbox allow-popups allow-forms; script-src 'none'"

// Theme is how a document is styled to match the web app: the stylesheet
// inlined into it, and the names it is told on <html>. Name is "paper" or
// "app", Scheme "light", "dark" or "" for none given; the caller checks
// them, as they reach the page as attribute values.
type Theme struct {
	Name, Scheme string
	CSS          []byte
}

// themeKey is the context key WithTheme stores a Theme under.
type themeKey struct{}

// WithTheme returns ctx carrying th, so the documents handler serves the
// request themed. A theme without a stylesheet is no theme.
func WithTheme(ctx context.Context, th Theme) context.Context {
	return context.WithValue(ctx, themeKey{}, th)
}

// themeFrom returns the theme ctx carries, and whether it carries one with
// a stylesheet.
func themeFrom(ctx context.Context) (Theme, bool) {
	th, ok := ctx.Value(themeKey{}).(Theme)
	return th, ok && len(th.CSS) > 0
}

// attrs is the theme's attributes for <html>, each with a leading space.
func (th *Theme) attrs() string {
	s := ` data-nh-theme="` + html.EscapeString(th.Name) + `"`
	if th.Scheme != "" {
		s += ` data-nh-scheme="` + html.EscapeString(th.Scheme) + `"`
	}
	return s
}

// style is the theme's stylesheet in a <style> element.
func (th *Theme) style() []byte {
	out := make([]byte, 0, len(th.CSS)+16)
	out = append(out, "<style>\n"...)
	out = append(out, th.CSS...)
	return append(out, "</style>"...)
}

// injectTheme returns doc, an HTML document, with th's attributes added to
// its <html> tag and its stylesheet first in its <head>, and nothing else
// changed. Only the document's start is looked at: its doctype, comments,
// whitespace and the <html> and <head> tags, up to the first other tag or
// text. A document without a <head> there gets the style after its <html>
// tag; one without an <html> tag gets both where its content begins.
func injectTheme(doc []byte, th *Theme) []byte {
	start := 0
	if bytes.HasPrefix(doc, []byte("\xef\xbb\xbf")) {
		start = 3
	}
	z := html.NewTokenizer(bytes.NewReader(doc[start:]))
	off := start
	htmlEnd, headEnd := -1, -1 // offsets just past each tag's name
	stop := len(doc)
	htmlClose := -1 // offset just past the <html> tag
scan:
	for {
		tt := z.Next()
		raw := len(z.Raw())
		switch tt {
		case html.ErrorToken:
			// Anything left, such as a tag cut off at the end, is
			// content: the theme goes before it.
			stop = off
			break scan
		case html.DoctypeToken, html.CommentToken:
		case html.TextToken:
			if len(bytes.TrimSpace(z.Raw())) > 0 {
				stop = off
				break scan
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			switch {
			case string(name) == "html" && htmlEnd < 0 && headEnd < 0:
				htmlEnd, htmlClose = off+len("<html"), off+raw
			case string(name) == "head" && headEnd < 0:
				headEnd = off + raw
				break scan
			default:
				stop = off
				break scan
			}
		default:
			stop = off
			break scan
		}
		off += raw
	}
	style := th.style()
	var out bytes.Buffer
	out.Grow(len(doc) + len(style) + 64)
	switch {
	case htmlEnd >= 0:
		out.Write(doc[:htmlEnd])
		out.WriteString(th.attrs())
		if headEnd >= 0 {
			out.Write(doc[htmlEnd:headEnd])
			out.Write(style)
			out.Write(doc[headEnd:])
		} else {
			out.Write(doc[htmlEnd:htmlClose])
			out.Write(style)
			out.Write(doc[htmlClose:])
		}
	case headEnd >= 0:
		// A <head> with no <html> before it: the element is added in
		// front of it.
		at := headEnd - len(z.Raw())
		out.Write(doc[:at])
		out.WriteString("<html" + th.attrs() + ">")
		out.Write(doc[at:headEnd])
		out.Write(style)
		out.Write(doc[headEnd:])
	default:
		if stop > len(doc) {
			stop = len(doc)
		}
		out.Write(doc[:stop])
		out.WriteString("<html" + th.attrs() + ">")
		out.Write(style)
		out.Write(doc[stop:])
	}
	return out.Bytes()
}

// noHomeKey is the context key WithoutHome stores under.
type noHomeKey struct{}

// WithoutHome returns ctx marking the request as one whose rendered Markdown
// has no link home, for when / is not a page a document's reader can load,
// as with the web app, whose frame policy refuses it.
func WithoutHome(ctx context.Context) context.Context {
	return context.WithValue(ctx, noHomeKey{}, true)
}

// withoutHomeFrom reports whether ctx was marked by WithoutHome.
func withoutHomeFrom(ctx context.Context) bool {
	v, _ := ctx.Value(noHomeKey{}).(bool)
	return v
}
