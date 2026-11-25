package report

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestNestedJSONHasNoErrorKey(t *testing.T) {
	m := setcap().With("port", 443).Wrap(errors.New("bind: permission denied"))
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["error"]; ok {
		t.Fatalf("nested message has an error key: %s", b)
	}
	want := `{"code":"port.needs-permission","level":"error","title":"HTTPS port 443 needs permission to bind.","detail":"On Linux …","fix":[{"text":"To give it permission, run:","command":"sudo setcap cap_net_bind_service=+ep '/p/nearhost'"}],"doctor":true,"data":{"port":443}}`
	if string(b) != want {
		t.Fatalf("JSON =\n%s\nwant\n%s", b, want)
	}
}

func TestNestedJSONOmitsEmptyFields(t *testing.T) {
	b, err := json.Marshal(Notef(PresetsNote, "A note."))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"code":"presets.note","level":"info","title":"A note."}`; string(b) != want {
		t.Fatalf("JSON = %s, want %s", b, want)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	m := Warnf(HostsPublic, "Name \x1b[31m<script>").Because("why").Step("do:", "cmd").Then("then").SuggestDoctor().With("path", "/x")
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var back Message
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Code != m.Code || back.Level != Warning || back.Title != m.Title || back.Why != "why" ||
		len(back.Fix) != 2 || back.Fix[0] != m.Fix[0] || !back.Doctor || back.Data["path"] != "/x" {
		t.Fatalf("round trip = %+v", back)
	}
}

func TestProblemFields(t *testing.T) {
	p := Problem(setcap(), 403, DocsBase)
	if p["type"] != "https://nearhost.dev/docs/reference/errors/#port.needs-permission" ||
		p["status"] != 403 || p["error"] != "HTTPS port 443 needs permission to bind." ||
		p["title"] != "HTTPS port 443 needs permission to bind." || p["detail"] != "On Linux …" ||
		p["code"] != PortNeedsPermission || p["level"] != "error" || p["doctor"] != true {
		t.Fatalf("Problem = %#v", p)
	}
}

func TestProblemHasErrorFirst(t *testing.T) {
	w := httptest.NewRecorder()
	WriteProblem(w, setcap(), nil)
	body := w.Body.String()
	if !strings.HasPrefix(body, `{"error":"HTTPS port 443 needs permission to bind."`) {
		t.Fatalf("body = %s", body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	e, _ := Lookup(PortNeedsPermission)
	if w.Code != e.Status {
		t.Fatalf("status = %d, want the catalog's %d", w.Code, e.Status)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, body)
	}
	if got["status"] != float64(e.Status) {
		t.Fatalf("status field = %v", got["status"])
	}
	if strings.Count(body, `"error":`) != 1 {
		t.Fatalf("body has more than one error key: %s", body)
	}
}

func TestProblemTypeLinksToCode(t *testing.T) {
	w := httptest.NewRecorder()
	WriteProblem(w, setcap(), nil)
	if !strings.Contains(w.Body.String(), `"type":"https://nearhost.dev/docs/reference/errors/#port.needs-permission"`) {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestWarningsComeLast(t *testing.T) {
	w := httptest.NewRecorder()
	WriteProblem(w, setcap(), []*Message{Warnf(HostsPublic, "First."), Notef(PresetsNote, "Second.")})
	body := strings.TrimSpace(w.Body.String())
	want := `,"warnings":[{"code":"hosts.public","level":"warning","title":"First."},{"code":"presets.note","level":"info","title":"Second."}]}`
	if !strings.HasSuffix(body, want) {
		t.Fatalf("body = %s\nwant it to end with %s", body, want)
	}
	if strings.Count(body, `"error":`) != 1 {
		t.Fatalf("a warning has an error key: %s", body)
	}

	w = httptest.NewRecorder()
	WriteProblem(w, setcap(), nil)
	if strings.Contains(w.Body.String(), "warnings") {
		t.Fatalf("no warnings, but the body has the key: %s", w.Body.String())
	}
}

func TestWriteProblemUnknownStatusIs500(t *testing.T) {
	w := httptest.NewRecorder()
	WriteProblem(w, Warnf(AdviceCode("x"), "Advice."), nil)
	if w.Code != 500 {
		t.Fatalf("status = %d, want 500 for a code with no status", w.Code)
	}
}

// bodyOf decodes a problem body and checks it holds the error first.
func bodyOf(t *testing.T, w *httptest.ResponseRecorder, title string) map[string]any {
	t.Helper()
	body := w.Body.String()
	if !strings.HasPrefix(body, `{"error":`+strconv.Quote(title)) {
		t.Fatalf("body does not start with the error: %q", body)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, body)
	}
	return got
}

func TestProblemSurvivesWarningDataThatDoesNotMarshal(t *testing.T) {
	w := httptest.NewRecorder()
	WriteProblem(w, setcap().With("port", 443),
		[]*Message{Warnf(HostsPublic, "Odd.").With("ch", make(chan int)), Notef(PresetsNote, "Fine.")})
	got := bodyOf(t, w, "HTTPS port 443 needs permission to bind.")
	if got["code"] != "port.needs-permission" {
		t.Fatalf("body lost the problem: %v", got)
	}
	ws, ok := got["warnings"].([]any)
	if !ok || len(ws) != 2 {
		t.Fatalf("warnings = %v, want both kept without their data", got["warnings"])
	}
}

func TestProblemSurvivesUnknownLevels(t *testing.T) {
	bad := Warnf(HostsPublic, "Odd.")
	bad.Level = Level(9)
	w := httptest.NewRecorder()
	WriteProblem(w, setcap(), []*Message{bad})
	got := bodyOf(t, w, "HTTPS port 443 needs permission to bind.")
	if got["code"] != "port.needs-permission" {
		t.Fatalf("body lost the problem: %v", got)
	}

	m := setcap()
	m.Level = Level(9)
	m.With("ch", make(chan int))
	w = httptest.NewRecorder()
	WriteProblem(w, m, []*Message{bad})
	if w.Body.Len() == 0 {
		t.Fatal("empty body")
	}
	bodyOf(t, w, "HTTPS port 443 needs permission to bind.")
}

func TestWriteProblemStatus(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("Retry-After", "5")
	WriteProblemStatus(w, Errorf(AgentsRefused, "refused; ask again in 5 s"), 429, nil)
	if w.Code != 429 {
		t.Fatalf("status = %d, want 429", w.Code)
	}
	got := bodyOf(t, w, "refused; ask again in 5 s")
	if got["status"] != float64(429) {
		t.Fatalf("status field = %v, want 429", got["status"])
	}
	if w.Header().Get("Retry-After") != "5" || w.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("headers = %v", w.Header())
	}
}

// Extension members go after the problem's own keys and before the
// warnings, and never replace a key the problem has.
func TestWriteProblemExt(t *testing.T) {
	w := httptest.NewRecorder()
	WriteProblemExt(w, Errorf(Unexpected, "the request has ended"), 409,
		map[string]any{"ended": true, "allowNotSaved": true, "error": "replaced", "bad": func() {}},
		[]*Message{Warnf(HostsPublic, "First.")})
	if w.Code != 409 {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	body := w.Body.String()
	got := bodyOf(t, w, "the request has ended")
	if got["ended"] != true || got["allowNotSaved"] != true {
		t.Errorf("body = %s, want both extension members", body)
	}
	if _, ok := got["bad"]; ok {
		t.Errorf("body = %s, holds a value that does not marshal", body)
	}
	if i, j, k := strings.Index(body, `"level"`), strings.Index(body, `"allowNotSaved"`), strings.Index(body, `"warnings"`); !(i < j && j < k) {
		t.Errorf("body = %s, want the extensions after the problem and before the warnings", body)
	}
}
