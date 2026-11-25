// Package htmlview shows a report.Message in a web page: the partial that
// Nearhost's own pages share (the status page, the approval pages), and
// whole pages for the proxy's error pages and the document server.
//
// It makes every piece of text printable and HTML-escapes it. Callers never
// clean a message's text first.
package htmlview

import (
	"bytes"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/nearhost/nearhost/internal/report"
	"github.com/nearhost/nearhost/internal/safetext"
)

// CSP is the policy of the pages Write sends unless the caller has set one:
// inline styles and data: images, and nothing that leaves the machine.
const CSP = "default-src 'none'; style-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Data is a message made ready for the templates: every text printable,
// split into what the partial shows.
type Data struct {
	// Level is "error", "warning" or "info"; the partial's class.
	Level string
	Code  string
	// Title is the context and the title.
	Title string
	// Paras are the explanation, one for each paragraph.
	Paras []string
	// Verbatim is the quoted lines, joined by line breaks; "" for none.
	Verbatim string
	Steps    []Step
	// Doctor is the line that suggests nearhost doctor; "" for none.
	Doctor string
	// Causes are what went wrong underneath, outermost first.
	Causes []string
	// Stay marks a message the page must not refresh away (a form's answer).
	Stay bool
}

// Step is a fix step: what it does, then the command.
type Step struct{ Text, Command string }

// Prepare makes m ready for the partial.
func Prepare(m *report.Message) Data {
	d := Data{Level: m.Level.String(), Code: safetext.Line(string(m.Code)), Title: report.Headline(m)}
	for _, p := range strings.Split(safetext.Lines(m.Why), "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			d.Paras = append(d.Paras, p)
		}
	}
	if len(m.Verbatim) > 0 {
		d.Verbatim = strings.Join(report.VerbatimLines(m), "\n")
	}
	for _, s := range m.Fix {
		st := Step{Text: strings.TrimSpace(safetext.Lines(s.Text)), Command: safetext.Line(s.Command)}
		if st.Text != "" || st.Command != "" {
			d.Steps = append(d.Steps, st)
		}
	}
	if m.Doctor {
		d.Doctor = report.DoctorLine
	}
	d.Causes = report.Causes(m)
	return d
}

// Stayed returns d marked as one the page must not refresh away.
func (d Data) Stayed() Data {
	d.Stay = true
	return d
}

const partials = `
{{define "message-styles"}}` + Styles + `{{end}}
{{define "message"}}<div class="message message--{{.Level}}" data-code="{{.Code}}"{{if .Stay}} role="alert" data-stay{{end}}>
  <p class="message__title">{{.Title}}</p>
  {{template "message-body" .}}
</div>{{end}}
{{define "message-body"}}
  {{- range .Paras}}<p>{{.}}</p>
  {{end -}}
  {{- if .Verbatim}}<pre class="message__verbatim">{{.Verbatim}}</pre>
  {{end -}}
  {{- range .Steps}}{{if .Text}}<p>{{.Text}}</p>
  {{end}}{{if .Command}}<p class="message__command"><code>{{.Command}}</code></p>
  {{end}}{{end -}}
  {{- if .Doctor}}<p>{{.Doctor}}</p>
  {{end -}}
  {{- range .Causes}}<p class="message__cause">{{.}}</p>
  {{end -}}
{{end}}`

// Template returns the templates the pages share: "message", which shows a
// Data in a box with its title, and "message-body", which shows all but the
// title. Their class names are message message--error|warning|info; the
// styles are Styles.
func Template() *template.Template {
	return template.Must(template.New("htmlview").Parse(partials))
}

// Styles is the CSS of the partial, for a page to include.
const Styles = `
.message { margin-top: 24px; padding: 12px 16px; border: 1px solid var(--line, #2A312B); border-left: 3px solid #F2A65A; border-radius: 8px; color: var(--text, #ECEFE8); }
.message--warning { border-left-color: #E8D26A; }
.message--info { border-left-color: var(--accent, #D6F26B); }
.message p { margin: 8px 0 0; max-width: none; }
.message > :first-child, .message > p.message__title { margin-top: 0; }
.message__title { font-weight: 600; color: var(--text, #ECEFE8); }
.message__cause { color: var(--muted, #A4ADA5); }
.message__command { margin-top: 4px; }
.message__command code, .message__verbatim { font: 15px ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--accent, #D6F26B); }
.message__verbatim { margin: 8px 0 0; padding: 8px 12px; overflow-x: auto; white-space: pre; background: var(--surface, #161A17); border-radius: 6px; color: var(--text, #ECEFE8); }
`

const pageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark">
<title>{{.Page}}</title>
<link rel="icon" type="image/svg+xml" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 64 64'%3E%3Crect width='64' height='64' rx='14' fill='%230F1210'/%3E%3Cpath d='M18 52V34a14 14 0 0 1 28 0v18' fill='none' stroke='%23D6F26B' stroke-width='10' stroke-linecap='round'/%3E%3Ccircle cx='32' cy='46' r='5.5' fill='%23ECEFE8'/%3E%3C/svg%3E">
<style>
:root {
  --ground: #0F1210;
  --surface: #161A17;
  --line: #2A312B;
  --text: #ECEFE8;
  --muted: #A4ADA5;
  --accent: #D6F26B;
  --display: "Space Grotesk", ui-sans-serif, system-ui, sans-serif;
  --body: "DM Sans", ui-sans-serif, system-ui, sans-serif;
}
*, *::before, *::after { box-sizing: border-box; }
html { background: var(--ground); }
body {
  margin: 0;
  min-height: 100vh;
  min-height: 100dvh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 48px 16px;
  background: var(--ground);
  color: var(--text);
  font-family: var(--body);
  font-size: 17px;
  line-height: 1.6;
  -webkit-font-smoothing: antialiased;
  overflow-wrap: break-word;
}
main { width: 100%; max-width: 34rem; }
.brand { display: flex; align-items: center; gap: 16px; }
.brand svg { width: 64px; height: 64px; flex: none; }
.wordmark {
  font-family: var(--display);
  font-weight: 600;
  font-size: 32px;
  letter-spacing: -0.03em;
  line-height: 1;
}
h1 {
  margin: 40px 0 0;
  font-family: var(--display);
  font-weight: 700;
  font-size: clamp(32px, 7vw, 44px);
  line-height: 1.1;
  letter-spacing: -0.03em;
  text-wrap: balance;
}
p { margin: 20px 0 0; color: var(--muted); max-width: 32rem; text-wrap: pretty; }
p + p { margin-top: 8px; }
.body { margin-top: 12px; }
.body pre { margin: 20px 0 0; }
.body .message__command code, .body .message__verbatim { font: 15px ui-monospace, SFMono-Regular, Menlo, monospace; color: var(--accent); }
.body .message__verbatim { padding: 8px 12px; overflow-x: auto; white-space: pre; background: var(--surface); border-radius: 6px; color: var(--text); }
</style>
</head>
<body>
<main>
  <div class="brand">
    <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" aria-hidden="true">
      <rect width="64" height="64" rx="14" fill="#0F1210"/>
      <path d="M18 52V34a14 14 0 0 1 28 0v18" fill="none" stroke="#D6F26B" stroke-width="10" stroke-linecap="round"/>
      <circle cx="32" cy="46" r="5.5" fill="#ECEFE8"/>
    </svg>
    <span class="wordmark">nearhost</span>
  </div>
  <h1>{{.Title}}</h1>
  <div class="body message--{{.Level}}" data-code="{{.Code}}">
  {{template "message-body" .}}</div>
</main>
</body>
</html>
`

var fullPage = template.Must(Template().New("page").Parse(pageHTML))

// Render returns the whole page for m, as the proxy's and the document
// server's pages show it: Nearhost's mark, the title as the heading, then
// the rest of the message. page is the tab's title; "" means the title.
func Render(m *report.Message, page string) []byte {
	d := Prepare(m)
	if page == "" {
		page = d.Title
	}
	var buf bytes.Buffer
	err := fullPage.Execute(&buf, struct {
		Data
		Page string
	}{d, page})
	if err != nil {
		return []byte(d.Title + "\n")
	}
	return buf.Bytes()
}

// WriteHTML answers with m as a whole page and status, with no body for a
// HEAD (r may be nil). The caller sets any other headers first; a page with
// no Content-Security-Policy gets CSP.
func WriteHTML(w http.ResponseWriter, r *http.Request, m *report.Message, status int, page string) {
	body := Render(m, page)
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("X-Content-Type-Options", "nosniff")
	if h.Get("Content-Security-Policy") == "" {
		h.Set("Content-Security-Policy", CSP)
	}
	w.WriteHeader(status)
	if r == nil || r.Method != http.MethodHead {
		w.Write(body)
	}
}

// WantsHTML reports whether r is from a browser: its Accept header names
// text/html.
func WantsHTML(r *http.Request) bool {
	return r != nil && strings.Contains(strings.ToLower(strings.Join(r.Header.Values("Accept"), ",")), "text/html")
}

// Write answers r with m and status: a whole page for a request that wants
// HTML, else plain text, "Title", a blank line and "Why". page is the tab's
// title for the page.
func Write(w http.ResponseWriter, r *http.Request, m *report.Message, status int, page string) {
	if WantsHTML(r) {
		WriteHTML(w, r, m, status, page)
		return
	}
	text := report.Headline(m) + "\n"
	if why := strings.TrimSpace(safetext.Lines(m.Why)); why != "" {
		text = report.Headline(m) + "\n\n" + why + "\n"
	}
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(text)))
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if r == nil || r.Method != http.MethodHead {
		w.Write([]byte(text))
	}
}
