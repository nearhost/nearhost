package htmlview

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/report"
)

func execPartial(t *testing.T, m *report.Message) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Template().ExecuteTemplate(&buf, "message", Prepare(m)); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// Hostile text is inert: markup is escaped, escape sequences and line
// breaks are gone, and verbatim lines keep their indentation.
func TestMessagePartialEscapes(t *testing.T) {
	m := report.Errorf(report.Unexpected, "<script>alert(1)</script>\x1b[31m red").
		Because("a\x1b]0;title\x07b <img src=x onerror=y>").
		Quote("  indented <b>", "line\nbreak").
		Step("Run <i>this</i>:", "echo '<script>'")
	m.Context = "start <web>"
	out := execPartial(t, m)
	for _, bad := range []string{"<script", "<img", "<b>", "<i>", "\x1b", "\x07"} {
		if strings.Contains(out, bad) {
			t.Errorf("partial holds %q:\n%s", bad, out)
		}
	}
	for _, want := range []string{
		"start &lt;web&gt;: &lt;script&gt;alert(1)&lt;/script&gt;",
		"&lt;img src=x onerror=y&gt;",
		"  indented &lt;b&gt;",
		"echo &#39;&lt;script&gt;&#39;",
		`class="message message--error"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("partial lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "line\nbreak") {
		t.Errorf("a verbatim line kept its line break:\n%s", out)
	}
}

func TestPartialShowsStepsAndDoctor(t *testing.T) {
	m := report.Warnf(report.HostsNeedsUpdate, "The hosts file is out of date").
		Because("It does not list shop.test.").
		Then("Reload once it is back.").
		Step("Update it.", "nearhost hosts").
		SuggestDoctor()
	out := execPartial(t, m)
	for _, want := range []string{
		`class="message message--warning"`,
		"<p>It does not list shop.test.</p>",
		"<p>Reload once it is back.</p>",
		"<p>Update it.</p>",
		"<code>nearhost hosts</code>",
		"Run `nearhost doctor`",
		`data-code="hosts.needs-update"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("partial lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(execPartial(t, report.Notef(report.UpdateAvailable, "x")), "message--info") {
		t.Error("a notice lacks its class")
	}
}

func TestNonHTMLRequestGetsPlainText(t *testing.T) {
	m := report.Errorf(report.Unexpected, "It <broke>\x1b[1m").Because("Because <x>.")
	for _, accept := range []string{"application/json", "text/plain"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept", accept)
		w := httptest.NewRecorder()
		Write(w, r, m, http.StatusBadGateway, "Nearhost")
		if w.Code != 502 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
			t.Fatalf("%s: %d %q", accept, w.Code, w.Header().Get("Content-Type"))
		}
		if got, want := w.Body.String(), "It <broke>[1m\n\nBecause <x>.\n"; got != want {
			t.Errorf("%s: body %q, want %q", accept, got, want)
		}
	}
	// A browser gets the page, and a HEAD gets no body.
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept", "text/html,application/xhtml+xml")
	w := httptest.NewRecorder()
	Write(w, r, m, 502, "Nearhost")
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") || !strings.Contains(w.Body.String(), "<h1>It &lt;broke&gt;[1m</h1>") {
		t.Errorf("html: %q\n%s", w.Header().Get("Content-Type"), w.Body.String())
	}
	r = httptest.NewRequest("HEAD", "/", nil)
	r.Header.Set("Accept", "text/html")
	w = httptest.NewRecorder()
	Write(w, r, m, 502, "Nearhost")
	if w.Code != 502 || w.Body.Len() != 0 {
		t.Errorf("head: %d, %d body bytes", w.Code, w.Body.Len())
	}
}
