package discovery

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ComposePort is a host-published port from a Compose file.
type ComposePort struct {
	Service   string
	Published int
	Target    int    // container port, 0 if unknown
	Var       string // variable that set the published port, if any
}

var composeFiles = []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}

// ReadCompose returns the first Compose file found and its name, so that a
// caller can point at the file it read. When there is none, name is empty and
// err is nil.
func ReadCompose(r FileReader) (name string, data []byte, err error) {
	for _, name := range composeFiles {
		data, err := r.ReadFile(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", nil, fmt.Errorf("read %s: %w", name, err)
		}
		return name, data, nil
	}
	return "", nil, nil
}

type composeDoc struct {
	Services map[string]composeService `yaml:"services"`
}

type composeService struct {
	Ports []yaml.Node `yaml:"ports"`
}

type composeLongPort struct {
	Published any    `yaml:"published"`
	Target    any    `yaml:"target"`
	Protocol  string `yaml:"protocol"`
}

// composeVar matches ${VAR}, ${VAR:-default}, ${VAR-default} and $VAR.
var composeVar = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:?-)([^}]*))?\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// Expand substitutes variables in s. It returns the first variable name
// seen and ok=false when a variable has no value and no default.
func Expand(s string, env map[string]string) (value, varName string, ok bool) {
	ok = true
	out := composeVar.ReplaceAllStringFunc(s, func(m string) string {
		sub := composeVar.FindStringSubmatch(m)
		name := sub[1]
		if name == "" {
			name = sub[4]
		}
		if varName == "" {
			varName = name
		}
		v, set := env[name]
		if set && (v != "" || sub[2] == "-") {
			return v // "-" keeps an explicitly empty value
		}
		if sub[2] != "" {
			return sub[3]
		}
		ok = false
		return ""
	})
	return out, varName, ok
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		return n, err == nil
	}
	return 0, false
}

// ParseCompose extracts published TCP ports, resolving variables against env.
// Entries that cannot be resolved, use ranges, or are UDP are skipped.
func ParseCompose(data []byte, env map[string]string) ([]ComposePort, error) {
	var doc composeDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse compose: %w", err)
	}
	var out []ComposePort
	for service, svc := range doc.Services {
		for _, node := range svc.Ports {
			switch node.Kind {
			case yaml.ScalarNode:
				if p, ok := parseShortPort(node.Value, env); ok {
					p.Service = service
					out = append(out, p)
				}
			case yaml.MappingNode:
				var lp composeLongPort
				if err := node.Decode(&lp); err != nil {
					continue
				}
				if lp.Protocol != "" && lp.Protocol != "tcp" {
					continue
				}
				pub, ok := toInt(lp.Published)
				varName := ""
				if !ok {
					if s, isStr := lp.Published.(string); isStr {
						resolved, vn, rok := Expand(s, env)
						varName = vn
						if !rok {
							continue
						}
						pub, ok = toInt(resolved)
					}
				}
				if !ok || pub < 1 || pub > 65535 {
					continue
				}
				if t, isStr := lp.Target.(string); isStr && strings.Contains(t, "-") {
					continue // target range
				}
				target, _ := toInt(lp.Target)
				out = append(out, ComposePort{Service: service, Published: pub, Target: target, Var: varName})
			}
		}
	}
	return out, nil
}

func parseShortPort(spec string, env map[string]string) (ComposePort, bool) {
	spec = strings.TrimSpace(spec)
	if strings.HasSuffix(spec, "/udp") {
		return ComposePort{}, false
	}
	spec = strings.TrimSuffix(spec, "/tcp")
	parts := splitOutsideBraces(spec)
	var hostPart, targetPart string
	switch len(parts) {
	case 3:
		hostPart, targetPart = parts[1], parts[2]
	case 2:
		hostPart, targetPart = parts[0], parts[1]
	default:
		return ComposePort{}, false // container-only port gets an ephemeral host port
	}
	resolvedHost, varName, ok := Expand(hostPart, env)
	if !ok || strings.Contains(resolvedHost, "-") {
		return ComposePort{}, false
	}
	published, err := strconv.Atoi(strings.TrimSpace(resolvedHost))
	if err != nil || published < 1 || published > 65535 {
		return ComposePort{}, false
	}
	target := 0
	if resolvedTarget, _, tok := Expand(targetPart, env); tok {
		if strings.Contains(resolvedTarget, "-") {
			return ComposePort{}, false // target range
		}
		target, _ = strconv.Atoi(strings.TrimSpace(resolvedTarget))
	}
	return ComposePort{Published: published, Target: target, Var: varName}, true
}

// splitOutsideBraces splits on ":" except inside ${...} and [...], so defaults
// such as ${PORT:-80} and IPv6 host addresses such as [::1] stay in one
// piece.
func splitOutsideBraces(s string) []string {
	var parts []string
	depth, start := 0, 0
	inBrackets := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '[' && depth == 0:
			inBrackets = true
		case s[i] == ']' && depth == 0:
			inBrackets = false
		case inBrackets:
		case s[i] == '$' && i+1 < len(s) && s[i+1] == '{':
			depth++
			i++
		case s[i] == '}' && depth > 0:
			depth--
		case s[i] == ':' && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}
