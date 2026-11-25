package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// listedPackage is what `go list -export -json` says of a package.
type listedPackage struct {
	ImportPath string
	Dir        string
	Export     string
	GoFiles    []string
	CgoFiles   []string
	Module     *struct{ Main bool }
}

var (
	listOnce   sync.Once
	listed     []listedPackage
	listedErr  error
	exportFile map[string]string // import path -> export data
)

// listModule runs `go list -deps -export` over the module once: the
// module's own packages for this platform, and the export data of every
// package they import, so the checker type-checks only the module's
// sources.
func listModule(t *testing.T) ([]listedPackage, map[string]string) {
	t.Helper()
	root := moduleRoot(t)
	listOnce.Do(func() {
		cmd := exec.Command("go", "list", "-deps", "-export", "-json=ImportPath,Dir,Export,GoFiles,CgoFiles,Module", "./...")
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			listedErr = fmt.Errorf("go list: %v\n%s", err, stderr.String())
			return
		}
		exportFile = map[string]string{}
		dec := json.NewDecoder(bytes.NewReader(out))
		for {
			var p listedPackage
			if err := dec.Decode(&p); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				listedErr = err
				return
			}
			if p.Export != "" {
				exportFile[p.ImportPath] = p.Export
			}
			if p.Module != nil && p.Module.Main {
				listed = append(listed, p)
			}
		}
	})
	if listedErr != nil {
		t.Fatal(listedErr)
	}
	return listed, exportFile
}

// typeCheck type-checks files, parsed into fset, as the package at path,
// importing from the export data in exports.
func typeCheck(fset *token.FileSet, path string, files []*ast.File, exports map[string]string) (*types.Package, *types.Info, error) {
	imp := importer.ForCompiler(fset, "gc", func(p string) (io.ReadCloser, error) {
		f, ok := exports[p]
		if !ok {
			return nil, fmt.Errorf("no export data for %s", p)
		}
		return os.Open(f)
	})
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Uses:  map[*ast.Ident]types.Object{},
		Defs:  map[*ast.Ident]types.Object{},
	}
	var errs []error
	conf := types.Config{Importer: imp, Error: func(err error) { errs = append(errs, err) }}
	pkg, _ := conf.Check(path, fset, files, info)
	return pkg, info, errors.Join(errs...)
}

// typedCodeViolations returns, as "file:line: what", each place in files
// where a report.Code is made other than from the catalog's constants
// (codes.go): a constant that is not one of them, such as a string literal
// or an untyped constant, given as a report.Code (an argument, a field, an
// assignment, a comparison, a return value), and any conversion to
// report.Code. The empty string, the zero Code that means no code, is not
// one. A constant of type report.Code declared elsewhere is the
// catalog's, as its own declaration is checked. name gives each file's name
// in the output.
func typedCodeViolations(fset *token.FileSet, files []*ast.File, info *types.Info, name func(*ast.File) string) []string {
	isCode := func(t types.Type) bool {
		n, ok := t.(*types.Named)
		return ok && n.Obj().Name() == "Code" && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == reportPath
	}
	// fromCatalog reports whether e names a constant of the catalog, or a
	// typed report.Code constant declared from one.
	fromCatalog := func(e ast.Expr) bool {
		var id *ast.Ident
		switch x := e.(type) {
		case *ast.Ident:
			id = x
		case *ast.SelectorExpr:
			id = x.Sel
		default:
			return false
		}
		c, ok := info.Uses[id].(*types.Const)
		if !ok {
			return false
		}
		if c.Pkg() != nil && c.Pkg().Path() == reportPath {
			return filepath.Base(fset.Position(c.Pos()).Filename) == "codes.go"
		}
		return isCode(c.Type())
	}
	var out []string
	for _, f := range files {
		file := name(f)
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if tv, ok := info.Types[x.Fun]; ok && tv.IsType() && isCode(tv.Type) {
					out = append(out, fmt.Sprintf("%s:%d: converts to report.Code; use a constant from internal/report/codes.go", file, fset.Position(x.Pos()).Line))
					return false
				}
			case ast.Expr:
				tv, ok := info.Types[x]
				if !ok || tv.Value == nil || !isCode(tv.Type) || fromCatalog(x) {
					return true
				}
				if tv.Value.Kind() == constant.String && constant.StringVal(tv.Value) == "" {
					// The zero Code, "no code", as in m.Code == "".
					return true
				}
				out = append(out, fmt.Sprintf("%s:%d: gives a report.Code the constant %s; use a constant from internal/report/codes.go",
					file, fset.Position(x.Pos()).Line, tv.Value.ExactString()))
				return false
			}
			return true
		})
	}
	return out
}

// checkModuleCodes type-checks every package of the module for this
// platform and returns its violations, and the files it read, relative to
// root with forward slashes.
func checkModuleCodes(t *testing.T, root string) (violations []string, read map[string]bool) {
	pkgs, exports := listModule(t)
	read = map[string]bool{}
	for _, p := range pkgs {
		if len(p.CgoFiles) > 0 {
			t.Fatalf("%s has cgo files, which the checker cannot type-check", p.ImportPath)
		}
		if len(p.GoFiles) == 0 {
			continue
		}
		fset := token.NewFileSet()
		var files []*ast.File
		rel := map[*ast.File]string{}
		for _, g := range p.GoFiles {
			path := filepath.Join(p.Dir, g)
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			r, _ := filepath.Rel(root, path)
			r = filepath.ToSlash(r)
			read[r] = true
			if r == "internal/report/codes.go" {
				// The one place codes are made; it is type-checked with
				// the package, not checked.
				rel[f] = ""
			} else {
				rel[f] = r
			}
			files = append(files, f)
		}
		_, info, err := typeCheck(fset, p.ImportPath, files, exports)
		if err != nil {
			t.Fatalf("type-check %s: %v", p.ImportPath, err)
		}
		var checked []*ast.File
		for _, f := range files {
			if rel[f] != "" {
				checked = append(checked, f)
			}
		}
		violations = append(violations, typedCodeViolations(fset, checked, info, func(f *ast.File) string { return rel[f] })...)
	}
	sort.Strings(violations)
	return violations, read
}

// TestTypedCodeCheckerFindsViolations type-checks a source that makes codes
// from strings in each way the syntax alone does not show, and one that
// keeps the rule.
func TestTypedCodeCheckerFindsViolations(t *testing.T) {
	_, exports := listModule(t)
	check := func(src string) []string {
		t.Helper()
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "x/x.go", src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		_, info, err := typeCheck(fset, "example.com/x", []*ast.File{f}, exports)
		if err != nil {
			t.Fatal(err)
		}
		return typedCodeViolations(fset, []*ast.File{f}, info, func(*ast.File) string { return "x/x.go" })
	}

	const bad = `package x

import "github.com/nearhost/nearhost/internal/report"

const madeUp = "made.up"

type row struct{ Code report.Code }

func take(report.Code) {}

func code() report.Code {
	return "made.up"
}

func f(m *report.Message) {
	take(madeUp)
	_ = row{Code: madeUp}
	if m.Code == "port.in-use" {
	}
	switch m.Code {
	case "crash":
	}
	m.Code = madeUp + ".x"
	_ = map[report.Code]bool{"x.y": true}
	_ = []report.Code{report.PortInUse, ("x.z")}
}
`
	v := check(bad)
	wantLines := []string{"x/x.go:12:", "x/x.go:16:", "x/x.go:17:", "x/x.go:18:", "x/x.go:21:", "x/x.go:23:", "x/x.go:24:", "x/x.go:25:"}
	if len(v) != len(wantLines) {
		t.Fatalf("got %d violations, want %d:\n%s", len(v), len(wantLines), strings.Join(v, "\n"))
	}
	for i, w := range wantLines {
		if !strings.HasPrefix(v[i], w) {
			t.Errorf("violation %d = %q, want it at %s", i, v[i], w)
		}
	}

	const good = `package x

import "github.com/nearhost/nearhost/internal/report"

const mine = report.PortInUse

type advice struct{ Code string }

type check struct{ code report.Code }

func f(id string, m *report.Message) {
	_ = report.Errorf(report.PortInUse, "Port %d is in use.", 80)
	_ = report.AdviceCode(id)
	_ = advice{Code: "loopback-clash"}
	_ = check{code: mine}
	if m.Code == report.Crash || m.Code == mine {
	}
	_ = string(m.Code) == "crash"
	if m.Code == "" {
	}
}
`
	if v := check(good); len(v) != 0 {
		t.Fatalf("good source has violations:\n%s", strings.Join(v, "\n"))
	}
}
