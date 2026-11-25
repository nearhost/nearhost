package doctor

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/report"
)

// releasedIDs are the finding IDs nearhost doctor has released, written out
// as agents match on them. They are codes, unchanged, and never change.
var releasedIDs = []string{
	"nearhostrc.pending", "nearhostrc.not-applied", "nearhostrc.gone", "nearhostrc.version",
	"nearhostrc.unset-variable", "nearhostrc.invalid",
	"routes.unserved-port", "routes.stale-port", "routes.unserved-host", "routes.not-listening",
	"presets.note",
	"hosts.dns-unchecked", "hosts.unresolved", "hosts.public", "hosts.unresolved-pattern", "hosts.overlap",
	"docker.compose-depth", "docker.host-gateway", "docker.compose-parse", "docker.compose-file",
	"docker.clients", "docker.ca",
	"projects.unchecked",
}

// Every released ID is a code of the catalog, spelt as it was released,
// and the findings this package makes use only those and the advice family.
func TestFindingIDsUnchanged(t *testing.T) {
	if len(releasedIDs) != 23 {
		t.Fatalf("%d IDs listed, want the 23 released", len(releasedIDs))
	}
	for _, id := range releasedIDs {
		c, ok := report.FindingCode(id)
		if !ok || string(c) != id {
			t.Errorf("FindingCode(%q) = %q, %v", id, c, ok)
		}
		if _, ok := report.Lookup(c); !ok {
			t.Errorf("%q is not in the catalog", id)
		}
		if got := (Finding{ID: id}).Complete().Code; string(got) != id {
			t.Errorf("finding %q has code %q", id, got)
		}
	}
	if c, ok := report.FindingCode("advice.loopback-clash"); !ok || c != report.AdviceCode("loopback-clash") {
		t.Errorf("advice code %q, %v", c, ok)
	}
	if _, ok := report.FindingCode("port.needs-permission"); ok {
		t.Error("a code that is not a finding's is taken as one")
	}

	// The IDs this package sets are the catalog's constants, so a finding's
	// ID can't drift from its code: no string literal is an ID.
	ids := map[string]int{}
	for _, name := range []string{"rc.go", "hosts.go", "docker.go"} {
		src, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range literalIDs(t, name, src, ids) {
			t.Error(v)
		}
	}
	if len(ids) != 3 || ids["rc.go"] < 9 {
		t.Errorf("found IDs set in %v, want rc.go (with its assignments), hosts.go and docker.go", ids)
	}
}

// literalIDs parses one Go file and returns each place it sets a field
// named ID to a string literal: as a key in a composite literal, or by an
// assignment to x.ID. It counts in ids every place the file sets an ID.
func literalIDs(t *testing.T, name string, src []byte, ids map[string]int) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	check := func(v ast.Expr) {
		ids[name]++
		for p, ok := v.(*ast.ParenExpr); ok; p, ok = v.(*ast.ParenExpr) {
			v = p.X
		}
		if lit, ok := v.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			s, _ := strconv.Unquote(lit.Value)
			out = append(out, name+": ID "+strconv.Quote(s)+" is a string literal; use the code's constant")
		} else if b, ok := v.(*ast.BinaryExpr); ok && b.Op == token.ADD {
			if lit, ok := b.X.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				out = append(out, name+": an ID is built from the string literal "+lit.Value+"; use the code's constant")
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.KeyValueExpr:
			if k, ok := x.Key.(*ast.Ident); ok && k.Name == "ID" {
				check(x.Value)
			}
		case *ast.AssignStmt:
			for i, l := range x.Lhs {
				if s, ok := l.(*ast.SelectorExpr); ok && s.Sel.Name == "ID" && i < len(x.Rhs) {
					check(x.Rhs[i])
				}
			}
		}
		return true
	})
	return out
}

// The guard finds a literal ID however it is set.
func TestLiteralIDsGuard(t *testing.T) {
	const src = `package doctor
func f() {
	_ = Finding{ID: "nearhostrc.x"}
	var fd Finding
	fd.ID = "nearhostrc.y"
	fd.ID = ("nearhostrc.z")
	fd.ID = "advice." + code
	fd.ID = string(report.NearhostrcInvalid)
}`
	ids := map[string]int{}
	got := literalIDs(t, "x.go", []byte(src), ids)
	if len(got) != 4 || ids["x.go"] != 5 {
		t.Errorf("found %d of 4 (%d IDs set): %v", len(got), ids["x.go"], got)
	}
}

// A finding is a message: its code is its ID, its title its message as a
// sentence, its step its fix, and the snippet is quoted as it is.
func TestFindingReport(t *testing.T) {
	f := Finding{
		ID: "nearhostrc.unset-variable", Severity: SeverityError, Project: "shop",
		Message: ".nearhostrc: line 3: APP_PORT is not set",
		File:    ".nearhostrc", Line: 3,
		Fix: Fix{Kind: FixProject, Text: "Set APP_PORT in .env.", Snippet: "APP_PORT=8000\nVITE_PORT=5173"},
	}
	m := f.Report()
	if m.Level != report.Error || m.Code != report.NearhostrcUnsetVariable ||
		m.Title != ".nearhostrc: line 3: APP_PORT is not set." ||
		!reflect.DeepEqual(m.Fix, []report.Step{{Text: "Set APP_PORT in .env."}}) ||
		!reflect.DeepEqual(m.Verbatim, []string{"APP_PORT=8000", "VITE_PORT=5173"}) ||
		m.Data["project"] != "shop" || m.Data["file"] != ".nearhostrc" || m.Data["line"] != 3 {
		t.Errorf("message %+v", m)
	}
	for sev, lv := range map[Severity]report.Level{SeverityWarning: report.Warning, SeverityInfo: report.Info} {
		if got := (Finding{ID: "presets.note", Severity: sev, Message: "x"}).Report().Level; got != lv {
			t.Errorf("%s: level %v", sev, got)
		}
	}

	// A message of several lines keeps its first as the title and the rest
	// as the explanation.
	g := Finding{ID: "nearhostrc.invalid", Severity: SeverityError, Message: "bad file\nline 2 is wrong\n"}.Complete()
	if g.Title != "bad file." || g.Detail != "line 2 is wrong" {
		t.Errorf("title %q, detail %q", g.Title, g.Detail)
	}
	// What a finding sets itself stays.
	h := Finding{ID: "presets.note", Message: "m", Title: "T.", Steps: []report.Step{{Text: "a", Command: "b"}}}.Complete()
	if h.Title != "T." || len(h.Steps) != 1 || h.Steps[0].Command != "b" {
		t.Errorf("%+v", h)
	}
}

// The JSON keeps every key agents read today, with today's values, and adds
// the message's.
func TestFindingJSONKeepsOldFields(t *testing.T) {
	f := Finding{
		ID: "routes.not-listening", Severity: SeverityWarning, Project: "myapp", Message: "nothing listens on 8080",
		File: "compose.yaml", Line: 4,
		Fix:  Fix{Kind: FixManual, Text: "Start the dev server.", Snippet: "x: 1"},
		Host: "kept.out",
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"id": "routes.not-listening", "severity": "warning", "project": "myapp", "message": "nothing listens on 8080",
		"file": "compose.yaml", "line": float64(4),
		"fix":  map[string]any{"kind": "manual", "text": "Start the dev server.", "snippet": "x: 1"},
		"code": "routes.not-listening", "title": "nothing listens on 8080.",
		"steps": []any{map[string]any{"text": "Start the dev server."}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("json %s\nwant %v", b, want)
	}
	// The old keys come first, in their old order.
	keys := []string{`"id"`, `"severity"`, `"project"`, `"message"`, `"file"`, `"line"`, `"fix"`, `"code"`, `"title"`, `"steps"`}
	last := -1
	for _, k := range keys {
		i := strings.Index(string(b), k)
		if i < last {
			t.Errorf("%s out of order in %s", k, b)
		}
		last = i
	}
	// No key the agent skill reads as a reply's own.
	for _, k := range []string{"error", "state", "api", "laptop", "held", "retry_after", "approve_url", "advice", "reason"} {
		if _, ok := got[k]; ok {
			t.Errorf("finding has %q", k)
		}
	}
	// It reads back.
	var back Finding
	if err := json.Unmarshal(b, &back); err != nil || back.ID != f.ID || back.Code != report.RoutesNotListening || !slices.Equal(back.Steps, []report.Step{{Text: "Start the dev server."}}) {
		t.Errorf("read back %+v, %v", back, err)
	}
}
