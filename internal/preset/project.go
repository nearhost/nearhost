package preset

import (
	"encoding/json"
	"regexp"
	"strconv"
)

// Reader reads files relative to a project root. Paths are slash separated.
// A missing file must return an error satisfying
// errors.Is(err, fs.ErrNotExist).
type Reader interface {
	ReadFile(name string) ([]byte, error)
}

// Project is everything a preset may see of a project.
type Project struct {
	Env     map[string]string
	Compose string // text of the Compose file, empty when there is none
	Files   Reader
}

// read returns a file inside the project. A path that leaves the project is
// treated as missing and never read.
func (pr *Project) read(name string) ([]byte, bool) {
	if pr.Files == nil || checkPath(name) != nil {
		return nil, false
	}
	data, err := pr.Files.ReadFile(name)
	return data, err == nil
}

func (pr *Project) hasFile(names []string) bool {
	for _, name := range names {
		if _, ok := pr.read(name); ok {
			return true
		}
	}
	return false
}

func (pr *Project) hasPackage(name string) bool {
	data, ok := pr.read("package.json")
	if !ok {
		return false
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return false
	}
	_, dep := pkg.Dependencies[name]
	_, dev := pkg.DevDependencies[name]
	return dep || dev
}

func (pr *Project) vars() map[string]any {
	env := pr.Env
	if env == nil {
		env = map[string]string{}
	}
	return map[string]any{"env": env, "compose": pr.Compose}
}

// DetectExpr evaluates a detect expression against a project. It is used
// for registry index entries, whose presets are not installed.
func DetectExpr(expr string, pr *Project) (bool, error) {
	env, err := newEnv(scopeDetect, host{file: pr.hasFile, pkg: pr.hasPackage})
	if err != nil {
		return false, err
	}
	return evalBool(env, expr, pr.vars())
}

// Detected reports whether the preset applies to the project.
func (p *Preset) Detected(pr *Project) (bool, error) {
	return DetectExpr(p.Detect, pr)
}

// EvalFacts scans the project for the preset's facts. A fact whose file or
// pattern is absent takes its empty value.
func (p *Preset) EvalFacts(pr *Project) map[string]any {
	out := make(map[string]any, len(p.Facts))
	for name, f := range p.Facts {
		out[name] = f.eval(pr)
	}
	return out
}

var quotedRe = regexp.MustCompile(`['"]([^'"]+)['"]`)

func (f Fact) eval(pr *Project) any {
	var m [][]byte
	for _, name := range f.Scan {
		if data, ok := pr.read(name); ok {
			m = f.re.FindSubmatch(data)
			break
		}
	}
	switch f.As {
	case "bool":
		return m != nil
	case "int":
		if m == nil {
			return int64(0)
		}
		n, err := strconv.ParseInt(string(m[1]), 10, 64)
		if err != nil {
			return int64(0)
		}
		return n
	case "string":
		if m == nil {
			return ""
		}
		return string(m[1])
	default: // quoted_list
		list := []string{}
		if m != nil {
			for _, q := range quotedRe.FindAllSubmatch(m[1], -1) {
				list = append(list, string(q[1]))
			}
		}
		return list
	}
}
