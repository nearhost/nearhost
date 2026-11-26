package report

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The guard keeps every error, warning and notice going through a Message:
// outside internal/report and the test tools, no code writes to standard
// error, answers HTTP with http.Error or a hand-made "error" body, labels
// text with "nearhost: ", or carries advice in a string field of its own.

// guardRule names a kind of old path.
type guardRule string

const (
	ruleStderr     guardRule = "stderr"      // os.Stderr, ErrOrStderr(), fmt.Fprint* to errOut or stderr
	ruleHTTPError  guardRule = "http-error"  // http.Error(…)
	ruleErrorBody  guardRule = "error-body"  // an "error" map key, or a field tagged json:"error"
	ruleLabel      guardRule = "label"       // a string literal that starts "nearhost: ", "Error: " or "Warning: "
	ruleAdviceText guardRule = "advice-text" // a struct field advice, fix or hint of type string
)

// guardHit is one old path: where, which rule, and the function or the
// type declared at package level it is in ("" for neither).
type guardHit struct {
	file string
	line int
	rule guardRule
	fn   string
	what string
}

func (h guardHit) String() string {
	in := ""
	if h.fn != "" {
		in = " in " + h.fn
	}
	return fmt.Sprintf("%s:%d: %s%s (%s)", h.file, h.line, h.what, in, h.rule)
}

// guardAllowed lists the old paths that stay, by file, rule and the
// function or type they are in, each with why: they pass another program's
// output through, are the one path's own outlet, or keep a released wire
// format that older clients read.
var guardAllowed = map[string]string{
	// Passthroughs of other programs.
	allowKey("internal/elevate/sudo_unix.go", ruleStderr, "sudoElevator.Run"):     "sudo's password prompt and the command's own output go to the user's terminal as they are",
	allowKey("internal/elevate/e2e_unix.go", ruleStderr, "e2eElevator.ask"):       "the e2e build's stand-in for sudo prompts on standard error as sudo does",
	allowKey("internal/cli/update.go", ruleStderr, "runUpdateCmd"):                "the installer's (brew, npm, go, the script) own output passes through as it is",
	allowKey("internal/cli/update.go", ruleStderr, "runAside"):                    "the Windows installer's own output passes through as it is",
	allowKey("internal/cli/supervise_windows.go", ruleStderr, "superviseService"): "the supervised core inherits the service process's standard error, which its log redirect then takes over",
	allowKey("internal/proxy/plainlog.go", ruleErrorBody, "proxyLine"):            "reads Caddy's JSON log lines, whose error key is Caddy's",
	// The one path's outlets, and progress that is not a message.
	allowKey("internal/cli/root.go", ruleStderr, "Main"):                     "writes the command's final error and the update notice through the command's messageOut: the term renderer, or JSON lines with --json",
	allowKey("internal/cli/root.go", ruleStderr, "commandSink"):              "the sink every command reports to: a messageOut on cmd's standard error",
	allowKey("internal/cli/doctor_stream.go", ruleStderr, "newDoctorStream"): "doctor's \"checking …\" progress line for a slow check, not a message",
	allowKey("internal/cli/doctor.go", ruleAdviceText, "check"):              "a doctor row's one-line fix, which fixSteps makes the message's step",
	allowKey("internal/cli/doctor_projects.go", ruleAdviceText, "jsonCheck"): "doctor --json's released fix key, beside the message's code and steps",
	// Released wire formats.
	allowKey("internal/agentapi/agentapi.go", ruleErrorBody, "Project"):       "the agent API's released project error, which the skill reads; problem carries the message",
	allowKey("internal/control/control.go", ruleErrorBody, "errorBody"):       "the control API's error key that clients before problem details read",
	allowKey("internal/control/actions.go", ruleErrorBody, "actionErrorBody"): "the control API's action error key that clients before problem details read",
	allowKey("internal/control/actions.go", ruleErrorBody, "decodeProblem"):   "reads the error key of a core from before problem details",
	allowKey("internal/control/control.go", ruleAdviceText, "EnvCheck"):       "the control API's released fix of an environment check; problem carries the message",
}

// oldLabels are the labels message text never starts with: renderers add
// the level's.
var oldLabels = []string{"nearhost: ", "Error: ", "Warning: "}

// allowKey is the guardAllowed key of a hit.
func allowKey(file string, rule guardRule, in string) string {
	return file + " " + string(rule) + " " + in
}

// guardSkipped reports whether the guard does not read the file at rel: the
// report package and its renderers, which are the one path, and the test
// tools.
func guardSkipped(rel string) bool {
	return strings.HasPrefix(rel, "internal/report/") || strings.HasPrefix(rel, "internal/testutil/")
}

func TestNoOldErrorPaths(t *testing.T) {
	root := moduleRoot(t)
	used := map[string]bool{}
	var bad []string
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
		if guardSkipped(rel) {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hits, err := guardHits(rel, src)
		if err != nil {
			return err
		}
		for _, h := range hits {
			k := allowKey(h.file, h.rule, h.fn)
			if _, ok := guardAllowed[k]; ok {
				used[k] = true
				continue
			}
			bad = append(bad, h.String())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bad {
		t.Errorf("%s; report it through a report.Message instead", b)
	}
	for k := range guardAllowed {
		if !used[k] {
			t.Errorf("guardAllowed has %q, which matches nothing now; remove it", k)
		}
	}
}

// guardHits parses one Go file and returns its old paths.
func guardHits(name string, src []byte) ([]guardHit, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	imported := map[string]string{} // local name -> import path
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		local := p[strings.LastIndex(p, "/")+1:]
		if imp.Name != nil {
			local = imp.Name.Name
		}
		imported[local] = p
	}
	// is reports whether e is pkg.id, pkg being the package at path.
	is := func(e ast.Expr, path, id string) bool {
		s, ok := e.(*ast.SelectorExpr)
		if !ok || s.Sel.Name != id {
			return false
		}
		x, ok := s.X.(*ast.Ident)
		return ok && imported[x.Name] == path
	}
	inAdviceAllowed := strings.HasPrefix(name, "internal/doctor/")

	var hits []guardHit
	fn := ""
	// reads are uses of os.Stderr that write nothing: os.Stderr.Fd(), as
	// a terminal check makes, and Stat and Name.
	reads := map[ast.Expr]bool{}
	add := func(n ast.Node, rule guardRule, what string) {
		hits = append(hits, guardHit{file: name, line: fset.Position(n.Pos()).Line, rule: rule, fn: fn, what: what})
	}
	var visit func(n ast.Node) bool
	visit = func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			fn = x.Name.Name
			if x.Recv != nil && len(x.Recv.List) > 0 {
				fn = recvName(x.Recv.List[0].Type) + "." + fn
			}
			if x.Body != nil {
				ast.Inspect(x.Body, visit)
			}
			fn = ""
			return false
		case *ast.TypeSpec:
			if fn == "" {
				fn = x.Name.Name
				ast.Inspect(x.Type, visit)
				fn = ""
				return false
			}
		case *ast.SelectorExpr:
			if is(x, "os", "Stderr") && !reads[x] {
				add(x, ruleStderr, "uses os.Stderr")
			}
		case *ast.CallExpr:
			if s, ok := x.Fun.(*ast.SelectorExpr); ok && is(s.X, "os", "Stderr") {
				switch s.Sel.Name {
				case "Fd", "Stat", "Name":
					reads[s.X] = true
				}
			}
			if s, ok := x.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "ErrOrStderr" {
				add(x, ruleStderr, "calls ErrOrStderr")
			}
			for _, p := range []string{"Fprint", "Fprintf", "Fprintln"} {
				if is(x.Fun, "fmt", p) && len(x.Args) > 0 {
					if w := lastName(x.Args[0]); w == "errOut" || w == "stderr" {
						add(x, ruleStderr, "prints to "+w)
					}
				}
			}
			if is(x.Fun, "net/http", "Error") {
				add(x, ruleHTTPError, "calls http.Error")
			}
		case *ast.KeyValueExpr:
			if lit, ok := x.Key.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, _ := strconv.Unquote(lit.Value); s == "error" {
					add(x, ruleErrorBody, `has an "error" key`)
				}
			}
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				if s, err := strconv.Unquote(x.Value); err == nil {
					for _, label := range oldLabels {
						if strings.HasPrefix(s, label) {
							add(x, ruleLabel, fmt.Sprintf("starts a string with %q", label))
						}
					}
				}
			}
		case *ast.StructType:
			for _, fld := range x.Fields.List {
				if fld.Tag != nil {
					tag, _ := strconv.Unquote(fld.Tag.Value)
					if j, ok := reflect.StructTag(tag).Lookup("json"); ok && strings.Split(j, ",")[0] == "error" {
						add(fld, ruleErrorBody, `has a field tagged json:"error"`)
					}
				}
				if inAdviceAllowed {
					continue
				}
				if typ, ok := fld.Type.(*ast.Ident); !ok || typ.Name != "string" {
					continue
				}
				for _, nm := range fld.Names {
					switch strings.ToLower(nm.Name) {
					case "advice", "fix", "hint":
						add(fld, ruleAdviceText, "has a string field "+nm.Name)
					}
				}
			}
		}
		return true
	}
	ast.Inspect(f, visit)
	return hits, nil
}

// recvName is the type name of a method's receiver.
func recvName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.StarExpr:
		return recvName(x.X)
	case *ast.IndexExpr:
		return recvName(x.X)
	case *ast.IndexListExpr:
		return recvName(x.X)
	case *ast.Ident:
		return x.Name
	}
	return ""
}

// lastName is the name e ends in: the identifier, or a selector's field.
func lastName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.ParenExpr:
		return lastName(x.X)
	}
	return ""
}

// TestGuardFindsOldPaths runs the guard on a source with each old path, and
// on one with none.
func TestGuardFindsOldPaths(t *testing.T) {
	const bad = `package x

import (
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

type body struct {
	Error string ` + "`json:\"error,omitempty\"`" + `
}

type tip struct {
	fix  string
	Hint string
}

func f(cmd *cobra.Command, w http.ResponseWriter, errOut *os.File) {
	fmt.Fprintln(os.Stderr, "x")
	fmt.Fprintln(cmd.ErrOrStderr(), "x")
	fmt.Fprintf(errOut, "x")
	http.Error(w, "x", 500)
	_ = map[string]any{"error": "x"}
	_ = fmt.Errorf("nearhost: x")
	_ = fmt.Sprintf("Error: x")
	_ = "Warning: x"
}
`
	hits, err := guardHits("x/bad.go", []byte(bad))
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		line int
		rule guardRule
	}{
		{12, ruleErrorBody}, {16, ruleAdviceText}, {17, ruleAdviceText},
		{21, ruleStderr}, {22, ruleStderr}, {23, ruleStderr}, {24, ruleHTTPError}, {25, ruleErrorBody}, {26, ruleLabel},
		{27, ruleLabel}, {28, ruleLabel},
	}
	var got []string
	for _, h := range hits {
		got = append(got, h.String())
	}
	if len(hits) != len(want) {
		t.Fatalf("got %d hits, want %d:\n%s", len(hits), len(want), strings.Join(got, "\n"))
	}
	for i, w := range want {
		if hits[i].line != w.line || hits[i].rule != w.rule {
			t.Errorf("hit %d = %s, want line %d, %s", i, got[i], w.line, w.rule)
		}
	}
	if hits[3].fn != "f" {
		t.Errorf("hit 3 is in %q, want f", hits[3].fn)
	}

	const good = `package x

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

type step struct{ Fix []string }

type row struct{ fix int }

func f(out io.Writer, stderr []byte) {
	fmt.Fprintln(out, "nearhost is running")
	_ = map[string]any{"errors": 1}
	_ = string(stderr)
	_ = term.IsTerminal(int(os.Stderr.Fd()))
}
`
	hits, err = guardHits("x/good.go", []byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("good source has hits: %v", hits)
	}
}
