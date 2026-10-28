package documents

import (
	"html/template"
	"net/http"
)

// pageTemplate is the small Nearhost page used when a tree file can't be
// shown. It uses the home page's colours and no scripts.
var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark">
<title>{{.Title}} · Nearhost</title>
<style>
body { margin: 0; min-height: 100vh; display: flex; align-items: center; justify-content: center;
  padding: 48px 16px; background: #0F1210; color: #ECEFE8;
  font: 17px/1.6 "DM Sans", ui-sans-serif, system-ui, sans-serif; overflow-wrap: anywhere; }
main { width: 100%; max-width: 34rem; }
h1 { margin: 0; font: 700 32px/1.15 "Space Grotesk", ui-sans-serif, system-ui, sans-serif; letter-spacing: -0.02em; }
p { margin: 20px 0 0; color: #A4ADA5; }
code { color: #D6F26B; font: 15px ui-monospace, SFMono-Regular, Menlo, monospace; }
</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
{{range .Lines}}<p>{{.}}</p>
{{end}}</main>
</body>
</html>
`))

// page is the data for pageTemplate. Title and Lines are trusted HTML built
// by the page functions from escaped values, so apostrophes stay readable.
type page struct {
	Title template.HTML
	Lines []template.HTML
}

// code escapes s and wraps it in <code>.
func code(s string) template.HTML {
	return template.HTML("<code>" + template.HTMLEscapeString(s) + "</code>")
}

// writePage sends p with status. The caller has set the isolation headers.
func writePage(w http.ResponseWriter, status int, p page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = pageTemplate.Execute(w, p)
}

// missingPage says the file at path on server isn't there any more.
func missingPage(w http.ResponseWriter, server, path string) {
	writePage(w, http.StatusNotFound, page{
		Title: "This file isn't there any more",
		Lines: []template.HTML{
			code(path) + " on " + code(server) + " isn't there any more. It may have been moved, renamed or deleted.",
		},
	})
}

// notConnectedPage says Nearhost isn't connected to server, and how to
// connect it.
func notConnectedPage(w http.ResponseWriter, server string) {
	name := template.HTMLEscapeString(server)
	writePage(w, http.StatusServiceUnavailable, page{
		Title: template.HTML("Nearhost isn't connected to " + name),
		Lines: []template.HTML{
			template.HTML("To read this file, select " + name + " in Nearhost and press c to connect it, then reload this page."),
		},
	})
}
