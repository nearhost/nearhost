package report

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
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
