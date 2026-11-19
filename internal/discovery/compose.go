package discovery

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"regexp"
	"slices"
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
// file then its override, as Compose merges them. An override's ports add
// to the service's; ports tagged !override replace them and ports tagged
// !reset clear them; a service tagged !reset is removed. A port a service
// publishes twice is listed once, as it was first seen. Ports are sorted by
// service. An error names the file that did not parse.
func ParseComposeFiles(files []ComposeFile, env map[string]string) ([]ComposePort, error) {
	merged := map[string][]ComposePort{}
	for _, f := range files {
		services, err := parseComposeServices(f.Data, env)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}
		for name, sp := range services {
			switch {
			case sp.removed:
				delete(merged, name)
			case sp.merge == tagReset || sp.merge == tagOverride:
				merged[name] = sp.ports
			default:
				merged[name] = append(merged[name], sp.ports...)
			}
		}
	}
	return flattenPorts(merged), nil
}

// flattenPorts lists the ports of services, sorted by service, each
// published port of a service once.
func flattenPorts(services map[string][]ComposePort) []ComposePort {
	var out []ComposePort
	for _, name := range slices.Sorted(maps.Keys(services)) {
		seen := map[int]bool{}
		for _, p := range services[name] {
			if !seen[p.Published] {
				seen[p.Published] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// Compose's merge tags, which an override file puts on a value to say how it
// merges with the Compose file's: !reset removes the value, !override
// replaces it.
const (
	tagReset    = "!reset"
	tagOverride = "!override"
)

// servicePorts is what one Compose file says about one service's ports.
type servicePorts struct {
	ports   []ComposePort
	merge   string // tagReset or tagOverride when the ports carry that tag
	removed bool   // the service itself is tagged !reset
}

// composeDoc is a Compose file. Its services stay nodes, so that a merge
// tag on a service, or a service that is not a mapping, can be seen.
type composeDoc struct {
	Services map[string]yaml.Node `yaml:"services"`
}

// composeService is the part of a service discovery reads. Ports stays a
// node, so that its merge tag can be seen.
type composeService struct {
	Ports yaml.Node `yaml:"ports"`
}

type composeLongPort struct {
	Published any    `yaml:"published"`
	Target    any    `yaml:"target"`
	Protocol  string `yaml:"protocol"`
}

// bracedVarPattern is the text of the braced forms ${VAR}, ${VAR:-default} and
// ${VAR-default}; its groups are the name, the operator and the default.
const bracedVarPattern = `\$\{([A-Za-z_][A-Za-z0-9_]*)(?:(:?-)([^}]*))?\}`

// bracedVar matches the braced forms only.
var bracedVar = regexp.MustCompile(bracedVarPattern)

// composeVar matches the braced forms and the unbraced $VAR, whose name is
// group 4.
var composeVar = regexp.MustCompile(bracedVarPattern + `|\$([A-Za-z_][A-Za-z0-9_]*)`)

// Expand substitutes variables in s, in both the braced and the unbraced form
// that Compose accepts. It returns the first variable name seen and ok=false
// when a variable has no value and no default.
func Expand(s string, env map[string]string) (value, varName string, ok bool) {
	return expand(composeVar, s, env)
}

// ExpandBraced substitutes only ${VAR}, ${VAR:-default} and ${VAR-default}
// in s and leaves a bare $VAR as written. A .nearhostrc uses it, since its
// documented form is the braced one and a label may hold a "$" in plain text.
// It returns the first variable name seen and ok=false when a variable has no
// value and no default.
func ExpandBraced(s string, env map[string]string) (value, varName string, ok bool) {
	return expand(bracedVar, s, env)
}

// expand replaces each match of re in s, whose submatches are the variable
// name (1 and, for an unbraced form, 4), the default operator (2) and the
// default (3).
func expand(re *regexp.Regexp, s string, env map[string]string) (value, varName string, ok bool) {
	ok = true
	out := re.ReplaceAllStringFunc(s, func(m string) string {
		sub := re.FindStringSubmatch(m)
		name := sub[1]
		if name == "" && len(sub) > 4 {
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
	services, err := parseComposeServices(data, env)
	if err != nil {
		return nil, err
	}
	byName := map[string][]ComposePort{}
	for name, sp := range services {
		if !sp.removed {
			byName[name] = sp.ports
		}
	}
	return flattenPorts(byName), nil
}

// parseComposeServices reads the published TCP ports of each service of one
// Compose file, with the merge tags on them. A service that is not a
// mapping, or whose keys do not decode, publishes none.
func parseComposeServices(data []byte, env map[string]string) (map[string]servicePorts, error) {
	var doc composeDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse compose: %w", err)
	}
	out := map[string]servicePorts{}
	for service, node := range doc.Services {
		n := &node
		for n.Kind == yaml.AliasNode && n.Alias != nil {
			n = n.Alias
		}
		if n.Tag == tagReset {
			out[service] = servicePorts{removed: true}
			continue
		}
		var svc composeService
		if n.Kind != yaml.MappingNode || n.Decode(&svc) != nil {
			out[service] = servicePorts{}
			continue
		}
		sp := servicePorts{}
		if t := svc.Ports.Tag; t == tagReset || t == tagOverride {
			sp.merge = t
		}
		if svc.Ports.Kind == yaml.SequenceNode && sp.merge != tagReset {
			for _, node := range svc.Ports.Content {
				for node.Kind == yaml.AliasNode && node.Alias != nil {
					node = node.Alias
				}
				if p, ok := parsePort(node, env); ok {
					p.Service = service
					sp.ports = append(sp.ports, p)
				}
			}
		}
		out[service] = sp
	}
	return out, nil
}

// parsePort reads one entry of a service's ports, in the short or the long
// syntax, resolving variables against env. It reports false for an entry
// that cannot be resolved, uses a range, or is UDP.
func parsePort(node *yaml.Node, env map[string]string) (ComposePort, bool) {
	switch node.Kind {
	case yaml.ScalarNode:
		return parseShortPort(node.Value, env)
	case yaml.MappingNode:
		var lp composeLongPort
		if err := node.Decode(&lp); err != nil {
			return ComposePort{}, false
		}
		if lp.Protocol != "" && lp.Protocol != "tcp" {
			return ComposePort{}, false
		}
		pub, ok := toInt(lp.Published)
		varName := ""
		if !ok {
			if s, isStr := lp.Published.(string); isStr {
				resolved, vn, rok := Expand(s, env)
				varName = vn
				if !rok {
					return ComposePort{}, false
				}
				pub, ok = toInt(resolved)
			}
		}
		if !ok || pub < 1 || pub > 65535 {
			return ComposePort{}, false
		}
		if t, isStr := lp.Target.(string); isStr && strings.Contains(t, "-") {
			return ComposePort{}, false // target range
		}
		target, _ := toInt(lp.Target)
		return ComposePort{Published: pub, Target: target, Var: varName}, true
	}
	return ComposePort{}, false
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
