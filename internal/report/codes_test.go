package report

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// codePattern is the shape of a code: <area>.<what>, lowercase, with
// hyphens. The general codes (generalCodes) are single words.
var codePattern = regexp.MustCompile(`^[a-z]+(\.[a-z0-9]+(-[a-z0-9]+)*)+$`)

var generalCodes = map[Code]bool{Unexpected: true, Crash: true, Interrupted: true}

func TestCatalogCodesAreUniqueAndWellFormed(t *testing.T) {
	seen := map[Code]bool{}
	for _, e := range Catalog() {
		if seen[e.Code] {
			t.Errorf("%q is in the catalog twice", e.Code)
		}
		seen[e.Code] = true
		if !codePattern.MatchString(string(e.Code)) && !generalCodes[e.Code] {
			t.Errorf("%q does not match %s", e.Code, codePattern)
		}
		if strings.HasPrefix(string(e.Code), "advice.") {
			t.Errorf("%q: the advice family is made by AdviceCode, not listed", e.Code)
		}
		if e.Level != Info && e.Level != Warning && e.Level != Error {
			t.Errorf("%q has no level", e.Code)
		}
		if e.Level == Error && (e.Status < 400 || e.Status > 599) {
			t.Errorf("%q is an error with status %d, want 400–599", e.Code, e.Status)
		}
		if e.Level != Error && e.Status != 0 && (e.Status < 400 || e.Status > 599) {
			t.Errorf("%q has status %d, want 0 or 400–599", e.Code, e.Status)
		}
		if strings.TrimSpace(e.Summary) == "" || strings.Contains(e.Summary, "\n") {
			t.Errorf("%q needs a one-line summary", e.Code)
		}
		if got, ok := Lookup(e.Code); !ok || !reflect.DeepEqual(got, e) {
			t.Errorf("Lookup(%q) = %+v, %v", e.Code, got, ok)
		}
	}
	if len(seen) == 0 {
		t.Fatal("the catalog is empty")
	}
}

// TestEveryCodeConstantIsCatalogued reads codes.go, so a constant added
// without a catalog entry fails.
func TestEveryCodeConstantIsCatalogued(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "codes.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, s := range g.Specs {
			vs := s.(*ast.ValueSpec)
			if typ, ok := vs.Type.(*ast.Ident); !ok || typ.Name != "Code" {
				continue
			}
			for i, name := range vs.Names {
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok {
					continue
				}
				n++
				code := Code(strings.Trim(lit.Value, `"`))
				if _, ok := Lookup(code); !ok {
					t.Errorf("%s (%q) is not in the catalog", name.Name, code)
				}
			}
		}
	}
	if n != len(Catalog()) {
		t.Errorf("codes.go has %d code constants and the catalog %d entries", n, len(Catalog()))
	}
}

func TestSeedCodes(t *testing.T) {
	for _, c := range []Code{
		"unexpected", "crash", "interrupted", "cli.usage", "port.needs-permission", "port.in-use",
		"core.already-running", "core.not-running", "core.stopped-while-starting", "core.still-starting",
		"core.lost", "core.api-mismatch", "core.other-config", "hosts.needs-update", "hosts.read-only",
		"ssh.unreachable", "ssh.auth-failed", "ssh.socket-path-too-long", "agents.not-ready",
		"agents.refused", "agents.path-too-long", "config.invalid", "config.unknown-keys",
		"service.unsupported", "service.no-user-manager", "service.copy", "service.as-root",
		"service.cannot-bind", "update.check-failed", "update.available", "interface.no-terminal",
	} {
		if _, ok := Lookup(c); !ok {
			t.Errorf("seed code %q is not in the catalog", c)
		}
	}
}

// TestDoctorIDsAreInTheCatalog lists nearhost doctor's finding IDs as
// released (internal/doctor, and projects.unchecked in the cli) with the
// level each has today. Where a finding's level depends on the situation,
// the table has the higher one.
func TestDoctorIDsAreInTheCatalog(t *testing.T) {
	ids := map[Code]Level{
		"nearhostrc.pending":        Warning,
		"nearhostrc.not-applied":    Warning,
		"nearhostrc.gone":           Warning,
		"nearhostrc.version":        Error,
		"nearhostrc.unset-variable": Error,
		"nearhostrc.invalid":        Error,
		"routes.unserved-port":      Warning,
		"routes.stale-port":         Info,
		"routes.unserved-host":      Warning,
		"routes.not-listening":      Warning, // info when the engine does not run the project
		"presets.note":              Info,
		"hosts.dns-unchecked":       Info,
		"hosts.unresolved":          Error,
		"hosts.public":              Warning,
		"hosts.unresolved-pattern":  Error,
		"hosts.overlap":             Warning,
		"docker.compose-depth":      Warning,
		"docker.host-gateway":       Warning,
		"docker.compose-parse":      Warning,
		"docker.compose-file":       Info,
		"docker.clients":            Warning, // info on macOS and Windows
		"docker.ca":                 Warning,
		"projects.unchecked":        Info,
	}
	if len(ids) != 23 {
		t.Fatalf("the table has %d IDs, want 23", len(ids))
	}
	for id, level := range ids {
		e, ok := Lookup(id)
		if !ok {
			t.Errorf("doctor ID %q is not in the catalog", id)
			continue
		}
		if e.Level != level {
			t.Errorf("%q has level %s, want %s", id, e.Level, level)
		}
	}
}

func TestAdviceCode(t *testing.T) {
	c := AdviceCode("loopback-clash")
	if c != "advice.loopback-clash" {
		t.Fatalf("AdviceCode = %q", c)
	}
	e, ok := Lookup(c)
	if !ok || e.Code != c || e.Level != Warning {
		t.Fatalf("Lookup(%q) = %+v, %v", c, e, ok)
	}
	if _, ok := Lookup("advice."); ok {
		t.Fatal("Lookup found an advice code with no ID")
	}
	if _, ok := Lookup("no.such-code"); ok {
		t.Fatal("Lookup found a code that is not in the catalog")
	}
}

// TestCodesOnlyFromCatalog checks the repository: outside codes.go, code
// must use the catalog's constants (or AdviceCode), never make a Code from
// a string. The module's packages for this platform are type-checked, so a
// string literal or untyped constant given as a report.Code anywhere is
// found; the files of other platforms and build tags, which are not, are
// read for the shapes the syntax shows.
func TestCodesOnlyFromCatalog(t *testing.T) {
	root := moduleRoot(t)
	bad, typed := checkModuleCodes(t, root)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if rel == "internal/report/codes.go" || typed[rel] {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		v, err := codeViolations(rel, src, strings.HasPrefix(rel, "internal/report/") && !strings.Contains(rel[len("internal/report/"):], "/"))
		if err != nil {
			return err
		}
		bad = append(bad, v...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(typed) == 0 {
		t.Fatal("the type-checked pass read no files")
	}
	for _, b := range bad {
		t.Errorf("%s", b)
	}
}

// TestCodeCheckerFindsViolations runs the checker on sources that break the
// rule in each way it knows, and on one that keeps it.
func TestCodeCheckerFindsViolations(t *testing.T) {
	const bad = `package x

import rp "github.com/nearhost/nearhost/internal/report"

var a = rp.Code("made.up")

const b rp.Code = "made.up"

func f(m *rp.Message, s string) {
	_ = rp.Errorf("made.up", "t")
	_ = rp.Warnf("made" + ".up", "t")
	_ = rp.Notef(("made.up"), "t")
	_, _ = rp.Lookup("made.up")
	_ = &rp.Message{Code: "made.up"}
	_ = rp.Entry{Code: "crash"}
	m.Code = "made.up"
	_ = rp.Code(s)
}
`
	v, err := codeViolations("x/bad.go", []byte(bad), false)
	if err != nil {
		t.Fatal(err)
	}
	wantLines := []string{
		"x/bad.go:5:", "x/bad.go:7:", "x/bad.go:10:", "x/bad.go:11:", "x/bad.go:12:", "x/bad.go:13:",
		"x/bad.go:14:", "x/bad.go:15:", "x/bad.go:16:", "x/bad.go:17:",
	}
	if len(v) != len(wantLines) {
		t.Fatalf("got %d violations, want %d:\n%s", len(v), len(wantLines), strings.Join(v, "\n"))
	}
	for i, w := range wantLines {
		if !strings.HasPrefix(v[i], w) {
			t.Errorf("violation %d = %q, want it at %s", i, v[i], w)
		}
	}

	const inReport = `package report

func g() *Message { return Errorf("made.up", "t").Wrap(nil) }

var c = Code("x.y")
`
	v, err = codeViolations("internal/report/g.go", []byte(inReport), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 2 {
		t.Fatalf("in the report package got %d violations, want 2:\n%s", len(v), strings.Join(v, "\n"))
	}

	const good = `package x

import "github.com/nearhost/nearhost/internal/report"

type advice struct{ Code string }

func f(id string) {
	_ = report.Errorf(report.PortInUse, "Port %d is in use.", 80)
	_ = report.AdviceCode(id)
	_ = advice{Code: "loopback-clash"}
	_ = fmtErrorf("made.up")
}

func fmtErrorf(string) error { return nil }
`
	v, err = codeViolations("x/good.go", []byte(good), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 0 {
		t.Fatalf("good source has violations:\n%s", strings.Join(v, "\n"))
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test")
		}
		dir = parent
	}
}
