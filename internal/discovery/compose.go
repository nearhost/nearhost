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

// composeFiles are the names Compose looks for, in its order of preference:
// the first one present is the project's Compose file. compose.yaml and
// compose.yml are the canonical names and the docker-compose ones are kept
// for older projects, the order compose-go's DefaultFileNames gives.
var composeFiles = []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"}

// composeOverrideFiles are the override names Compose looks for beside a
// Compose file, in its order of preference. Compose merges the first one
// present over the Compose file, whichever of composeFiles that is, as
// compose-go's DefaultOverrideFileNames and WithDefaultConfigPath do.
var composeOverrideFiles = []string{"compose.override.yml", "compose.override.yaml", "docker-compose.override.yml", "docker-compose.override.yaml"}

// ComposeFile is one Compose file of a project, by name.
type ComposeFile struct {
	Name string
	Data []byte
}

// ReadCompose returns the files Compose reads by default: the first
// Compose file found, then the first override file found beside it, if
// any. Both carry their names, so that a caller can point at the file it
// read. With no Compose file it returns none, and no error, even when an
// override file is present, as Compose reads an override only beside a
// Compose file.
func ReadCompose(r FileReader) ([]ComposeFile, error) {
	base, err := readFirst(r, composeFiles)
	if err != nil || base == nil {
		return nil, err
	}
	override, err := readFirst(r, composeOverrideFiles)
	if err != nil {
		return nil, err
	}
	if override == nil {
		return []ComposeFile{*base}, nil
	}
	return []ComposeFile{*base, *override}, nil
}

// readFirst reads the first of names that is present, or returns nil.
func readFirst(r FileReader, names []string) (*ComposeFile, error) {
	for _, file := range names {
		b, err := r.ReadFile(file)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, ReadError(file, err)
		}
		return &ComposeFile{Name: file, Data: b}, nil
	}
	return nil, nil
}

// ComposeText joins the text of files, the Compose file then its override,
// for the presets that look for words in a project's Compose setup.
func ComposeText(files []ComposeFile) string {
	parts := make([]string, 0, len(files))
	for _, f := range files {
		parts = append(parts, string(f.Data))
	}
	return strings.Join(parts, "\n")
}

// ParseComposeFiles extracts the published TCP ports of files, the Compose
// file then its override, as Compose merges them: an override's ports add
// to the service's. A port a service publishes in both files is listed
// once, as it was first seen. An error names the file that did not parse.
func ParseComposeFiles(files []ComposeFile, env map[string]string) ([]ComposePort, error) {
	type key struct {
		service   string
		published int
	}
	seen := map[key]bool{}
	var out []ComposePort
	for _, f := range files {
		ports, err := ParseCompose(f.Data, env)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}
		for _, p := range ports {
			k := key{p.Service, p.Published}
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, p)
		}
	}
	return out, nil
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
