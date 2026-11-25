package report

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
)

const reportPath = "github.com/nearhost/nearhost/internal/report"

// literalCode matches string literals that look like codes, for fields named
// Code whose type the checker cannot see.
var literalCode = regexp.MustCompile(`^([a-z]+(\.[a-z0-9]+(-[a-z0-9]+)*)+|unexpected|crash|interrupted)$`)

// codeTakers are the report functions whose first argument is a Code.
var codeTakers = map[string]bool{"Errorf": true, "Warnf": true, "Notef": true, "Lookup": true}

// codeViolations parses one Go file and returns, as "file:line: what", each
// place it makes a report.Code other than from the catalog's constants:
//   - a conversion report.Code(…), of anything;
//   - a string literal as the code of report.Errorf, Warnf, Notef or Lookup;
//   - a constant or variable of type report.Code given a string literal;
//   - a string literal that looks like a code given to a field named Code,
//     in a composite literal or an assignment.
//
// inReport says the file is in package report itself, where the names are
// not qualified. The check reads syntax only; it does not type-check.
func codeViolations(name string, src []byte, inReport bool) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	pkg := ""
	for _, imp := range f.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == reportPath {
			pkg = "report"
			if imp.Name != nil {
				pkg = imp.Name.Name
			}
		}
	}
	if pkg == "" && !inReport {
		return nil, nil
	}
	// isReport reports whether e names report's identifier id.
	isReport := func(e ast.Expr, id string) bool {
		switch x := e.(type) {
		case *ast.SelectorExpr:
			p, ok := x.X.(*ast.Ident)
			return ok && pkg != "" && p.Name == pkg && x.Sel.Name == id
		case *ast.Ident:
			return inReport && x.Name == id
		}
		return false
	}
	var out []string
	add := func(n ast.Node, what string) {
		out = append(out, fmt.Sprintf("%s:%d: %s; use a constant from internal/report/codes.go", name, fset.Position(n.Pos()).Line, what))
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if isReport(x.Fun, "Code") {
				add(x, "converts to report.Code")
				return true
			}
			for fn := range codeTakers {
				if isReport(x.Fun, fn) && len(x.Args) > 0 && isStringLiteral(x.Args[0]) {
					add(x, "gives "+fn+" a string literal as its code")
				}
			}
		case *ast.ValueSpec:
			if x.Type != nil && isReport(x.Type, "Code") {
				for _, v := range x.Values {
					if isStringLiteral(v) {
						add(v, "gives a report.Code a string literal")
					}
				}
			}
		case *ast.KeyValueExpr:
			if k, ok := x.Key.(*ast.Ident); ok && k.Name == "Code" && looksLikeCode(x.Value) {
				add(x, "sets a Code field to a string literal")
			}
		case *ast.AssignStmt:
			for i, l := range x.Lhs {
				if s, ok := l.(*ast.SelectorExpr); ok && s.Sel.Name == "Code" && i < len(x.Rhs) && looksLikeCode(x.Rhs[i]) {
					add(x, "sets a Code field to a string literal")
				}
			}
		}
		return true
	})
	return out, nil
}

// isStringLiteral reports whether e is a string literal or is made only of
// them (in parentheses or joined with +).
func isStringLiteral(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.BasicLit:
		return x.Kind == token.STRING
	case *ast.ParenExpr:
		return isStringLiteral(x.X)
	case *ast.BinaryExpr:
		return x.Op == token.ADD && isStringLiteral(x.X) && isStringLiteral(x.Y)
	}
	return false
}

// looksLikeCode reports whether e is a string literal shaped like a code.
func looksLikeCode(e ast.Expr) bool {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			break
		}
		e = p.X
	}
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	s, err := strconv.Unquote(lit.Value)
	return err == nil && literalCode.MatchString(strings.TrimSpace(s))
}
