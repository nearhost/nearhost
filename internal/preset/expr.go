package preset

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/ext"

	"github.com/nearhost/nearhost/internal/hostname"
)

// scope says which names an expression may use. Each scope adds to the one
// before it.
type scope int

const (
	scopeDetect scope = iota
	scopeRoute
	scopeAdvisory
)

// host answers what expressions ask about the project. A nil function
// answers false, or for freePort the port it was given.
type host struct {
	file     func(names []string) bool
	pkg      func(name string) bool
	active   func(name string) bool
	freePort func(from int) int
	env      map[string]string // for env_port
	hosts    []string          // for serves: every name the project is served at
}

// costLimit stops an expression that would run for too long.
const costLimit = 100000

// maxFileArgs is how many names file() takes. CEL has no variadic functions.
const maxFileArgs = 8

var stringList = reflect.TypeOf([]string{})

func newEnv(s scope, h host) (*cel.Env, error) {
	str := func(v ref.Val) string {
		s, _ := v.Value().(string)
		return s
	}
	fileOverloads := make([]cel.FunctionOpt, 0, maxFileArgs)
	for n := 1; n <= maxFileArgs; n++ {
		args := make([]*cel.Type, n)
		for i := range args {
			args[i] = cel.StringType
		}
		fileOverloads = append(fileOverloads, cel.Overload(fmt.Sprintf("file_%d", n), args, cel.BoolType,
			cel.FunctionBinding(func(vals ...ref.Val) ref.Val {
				names := make([]string, len(vals))
				for i, v := range vals {
					names[i] = str(v)
				}
				return types.Bool(h.file != nil && h.file(names))
			})))
	}
	opts := []cel.EnvOption{
		ext.Strings(),
		cel.Variable("env", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("compose", cel.StringType),
		cel.Function("file", fileOverloads...),
		cel.Function("pkg", cel.Overload("pkg_string", []*cel.Type{cel.StringType}, cel.BoolType,
			cel.UnaryBinding(func(v ref.Val) ref.Val {
				return types.Bool(h.pkg != nil && h.pkg(str(v)))
			}))),
	}
	if s >= scopeRoute {
		opts = append(opts,
			cel.Variable("facts", cel.MapType(cel.StringType, cel.DynType)),
			cel.Variable("presets", cel.MapType(cel.StringType, cel.MapType(cel.StringType, cel.DynType))),
			cel.Function("active", cel.Overload("active_string", []*cel.Type{cel.StringType}, cel.BoolType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					return types.Bool(h.active != nil && h.active(str(v)))
				}))),
			cel.Function("url_host", cel.Overload("url_host_string", []*cel.Type{cel.StringType}, cel.StringType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					host, _ := splitURL(str(v))
					return types.String(host)
				}))),
			cel.Function("url_port", cel.Overload("url_port_string", []*cel.Type{cel.StringType}, cel.IntType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					_, port := splitURL(str(v))
					return types.Int(port)
				}))),
			cel.Function("env_port", cel.Overload("env_port_string_int", []*cel.Type{cel.StringType, cel.IntType}, cel.IntType,
				cel.BinaryBinding(func(name, def ref.Val) ref.Val {
					if port := validPort(h.env[str(name)]); port != 0 {
						return types.Int(port)
					}
					return def
				}))),
		)
	}
	if s >= scopeAdvisory {
		opts = append(opts,
			cel.Variable("alias", cel.StringType),
			cel.Variable("hosts", cel.ListType(cel.StringType)),
			cel.Function("serves", cel.Overload("serves_string", []*cel.Type{cel.StringType}, cel.BoolType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					return types.Bool(serves(h.hosts, str(v)))
				}))),
			cel.Function("free_port", cel.Overload("free_port_int", []*cel.Type{cel.IntType}, cel.IntType,
				cel.UnaryBinding(func(v ref.Val) ref.Val {
					from, _ := v.Value().(int64)
					if h.freePort == nil {
						return types.Int(from)
					}
					return types.Int(h.freePort(int(from)))
				}))),
			cel.Function("host_in", cel.Overload("host_in_string_list", []*cel.Type{cel.StringType, cel.ListType(cel.StringType)}, cel.BoolType,
				cel.BinaryBinding(func(a, b ref.Val) ref.Val {
					list, err := b.ConvertToNative(stringList)
					if err != nil {
						return types.NewErr("host_in: %v", err)
					}
					return types.Bool(hostIn(str(a), list.([]string)))
				}))),
		)
	}
	return cel.NewEnv(opts...)
}

// splitURL returns the lower-case host and the port of a URL, or of a bare
// host or host:port, which people write as often. Anything it cannot parse
// gives an empty host and port 0, so a preset treats it as absent.
func splitURL(s string) (host string, port int) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		s = "//" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", 0
	}
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), "."), validPort(u.Port())
}

// validPort reads s as a port from 1 to 65535, and gives 0 for anything else.
func validPort(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return n
}

// serves reports whether name is one of hosts, or matches a pattern in it.
func serves(hosts []string, name string) bool {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if name == "" {
		return false
	}
	for _, h := range hosts {
		if hostname.Match(strings.ToLower(h), name) {
			return true
		}
	}
	return false
}

// hostIn reports whether host equals an entry of allowed, or matches an
// entry that starts with "." as a domain suffix.
func hostIn(host string, allowed []string) bool {
	for _, a := range allowed {
		if a == host {
			return true
		}
		if strings.HasPrefix(a, ".") && (strings.HasSuffix(host, a) || host == strings.TrimPrefix(a, ".")) {
			return true
		}
	}
	return false
}

// check compiles src and requires it to produce want.
func check(env *cel.Env, src string, want *cel.Type) error {
	ast, iss := env.Compile(src)
	if iss != nil && iss.Err() != nil {
		return iss.Err()
	}
	if t := ast.OutputType(); !t.IsExactType(want) && !t.IsExactType(cel.DynType) {
		return fmt.Errorf("must give %s, and gives %s", want, t)
	}
	return nil
}

func run(env *cel.Env, src string, vars map[string]any) (ref.Val, error) {
	ast, iss := env.Compile(src)
	if iss != nil && iss.Err() != nil {
		return nil, iss.Err()
	}
	prg, err := env.Program(ast, cel.CostLimit(costLimit))
	if err != nil {
		return nil, err
	}
	out, _, err := prg.Eval(vars)
	return out, err
}

func eval(env *cel.Env, src string, vars map[string]any) (any, error) {
	out, err := run(env, src, vars)
	if err != nil {
		return nil, err
	}
	return out.Value(), nil
}

func evalBool(env *cel.Env, src string, vars map[string]any) (bool, error) {
	v, err := eval(env, src, vars)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%q is not true or false", src)
	}
	return b, nil
}

func evalInt(env *cel.Env, src string, vars map[string]any) (int, error) {
	v, err := eval(env, src, vars)
	if err != nil {
		return 0, err
	}
	n, ok := v.(int64)
	if !ok {
		return 0, fmt.Errorf("%q is not a whole number", src)
	}
	return int(n), nil
}

func evalStrings(env *cel.Env, src string, vars map[string]any) ([]string, error) {
	out, err := run(env, src, vars)
	if err != nil {
		return nil, err
	}
	list, err := out.ConvertToNative(stringList)
	if err != nil {
		return nil, fmt.Errorf("%q is not a list of text: %w", src, err)
	}
	return list.([]string), nil
}

// part is a piece of a template: literal text or an expression.
type part struct {
	text string
	expr bool
}

// splitTemplate cuts s at its {{ expression }} parts.
func splitTemplate(s string) ([]part, error) {
	var parts []part
	for {
		open := strings.Index(s, "{{")
		if open < 0 {
			break
		}
		end := strings.Index(s[open:], "}}")
		if end < 0 {
			return nil, errors.New("a {{ has no }} after it")
		}
		parts = append(parts, part{text: s[:open]}, part{text: strings.TrimSpace(s[open+2 : open+end]), expr: true})
		s = s[open+end+2:]
	}
	return append(parts, part{text: s}), nil
}

func checkTemplate(env *cel.Env, tmpl string) error {
	parts, err := splitTemplate(tmpl)
	if err != nil {
		return err
	}
	for _, p := range parts {
		if !p.expr {
			continue
		}
		if _, iss := env.Compile(p.text); iss != nil && iss.Err() != nil {
			return iss.Err()
		}
	}
	return nil
}

// render fills in the {{ expression }} parts of tmpl.
func render(env *cel.Env, tmpl string, vars map[string]any) (string, error) {
	parts, err := splitTemplate(tmpl)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, p := range parts {
		if !p.expr {
			b.WriteString(p.text)
			continue
		}
		v, err := eval(env, p.text, vars)
		if err != nil {
			return "", err
		}
		fmt.Fprint(&b, v)
	}
	return b.String(), nil
}

// checkExpressions compiles every expression in the preset, so a mistake is
// found when the file is loaded and not when a project happens to reach it.
func (p *Preset) checkExpressions(bad func(string, ...any)) {
	envs := make([]*cel.Env, scopeAdvisory+1)
	for s := scopeDetect; s <= scopeAdvisory; s++ {
		env, err := newEnv(s, host{})
		if err != nil {
			bad("expressions: %w", err)
			return
		}
		envs[s] = env
	}
	if strings.TrimSpace(p.Detect) != "" {
		if err := check(envs[scopeDetect], p.Detect, cel.BoolType); err != nil {
			bad("detect: %w", err)
		}
	}
	for i, r := range p.Routes {
		if r.When != "" {
			if err := check(envs[scopeRoute], r.When, cel.BoolType); err != nil {
				bad("routes[%d]: when: %w", i, err)
			}
		}
		if strings.TrimSpace(r.Port) != "" {
			if err := check(envs[scopeRoute], r.Port, cel.IntType); err != nil {
				bad("routes[%d]: port: %w", i, err)
			}
		}
		if strings.TrimSpace(r.Hosts) != "" {
			if err := check(envs[scopeRoute], r.Hosts, cel.ListType(cel.StringType)); err != nil {
				bad("routes[%d]: hosts: %w", i, err)
			}
		}
	}
	for i, a := range p.Advisories {
		if strings.TrimSpace(a.When) != "" {
			if err := check(envs[scopeAdvisory], a.When, cel.BoolType); err != nil {
				bad("advisories[%d]: when: %w", i, err)
			}
		}
		if err := checkTemplate(envs[scopeAdvisory], a.Message); err != nil {
			bad("advisories[%d]: message: %w", i, err)
		}
		if err := checkTemplate(envs[scopeAdvisory], a.Snippet); err != nil {
			bad("advisories[%d]: snippet: %w", i, err)
		}
	}
}
