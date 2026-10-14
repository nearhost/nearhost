// Package preset loads presets: data files that tell discovery what a
// framework's ports mean and what advice to give about them.
package preset

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/nearhost/nearhost/internal/config"
)

// SchemaVersion is the newest preset format this build understands.
const SchemaVersion = 1

// ErrNewerSchema reports a preset written for a later nearhost.
var ErrNewerSchema = errors.New("preset needs a newer nearhost")

// WebHint says how strongly a preset suggests a route as the web route.
type WebHint string

const (
	// WebNone gives no hint.
	WebNone WebHint = ""
	// WebPreferred is chosen before the core rules.
	WebPreferred WebHint = "preferred"
	// WebFallback is chosen when no container port 80 is published.
	WebFallback WebHint = "fallback"
)

// ComposePorts is the one value of a preset's compose field: while the
// preset is active, discovery proposes the Compose file's published ports.
const ComposePorts = "ports"

// Preset is one parsed preset file.
type Preset struct {
	Schema      int             `yaml:"schema"`
	Name        string          `yaml:"name"`
	Title       string          `yaml:"title"`
	Description string          `yaml:"description"`
	After       []string        `yaml:"after"`
	Detect      string          `yaml:"detect"`
	Compose     string          `yaml:"compose"`
	Ports       []Port          `yaml:"ports"`
	Facts       map[string]Fact `yaml:"facts"`
	Routes      []Route         `yaml:"routes"`
	Suppress    []string        `yaml:"suppress"`
	Advisories  []Advisory      `yaml:"advisories"`

	// Source is the alias of the registry the preset was installed from, and
	// empty for a local file. Whoever loads the preset sets it; a preset file
	// cannot.
	Source string `yaml:"-"`
}

// Key is the preset's name among all enabled presets: its own name for a
// local file, <alias>/<name> for a preset from a registry.
func (p *Preset) Key() string {
	if p.Source == "" {
		return p.Name
	}
	return p.Source + "/" + p.Name
}

// Resolve returns the key of the preset that p means by name in `after`,
// `active()` or `presets[]`. In a preset from a registry a plain name is a
// preset from the same registry, and a qualified name means nothing, so the
// result is empty. In a local preset a plain name is a local preset and a
// qualified name is that registry's preset.
func (p *Preset) Resolve(name string) string {
	switch {
	case p.Source == "":
		return name
	case strings.Contains(name, "/"):
		return ""
	}
	return p.Source + "/" + name
}

// NameOf is how p refers to other, the reverse of Resolve. ok is false when
// p cannot refer to other at all: a preset from a registry sees only the
// presets of its own registry.
func (p *Preset) NameOf(other *Preset) (name string, ok bool) {
	switch {
	case p.Source == "":
		return other.Key(), true
	case other.Source == p.Source:
		return other.Name, true
	}
	return "", false
}

// Port maps a variable that names a port to a label and role. Its id is Var.
type Port struct {
	Var   string      `yaml:"var"`
	Label string      `yaml:"label"`
	Role  config.Role `yaml:"role"`
	Web   WebHint     `yaml:"web"`
}

// Fact is a value scanned from the first file in Scan that exists.
type Fact struct {
	Scan  []string `yaml:"scan"`
	Regex string   `yaml:"regex"`
	As    string   `yaml:"as"`

	re *regexp.Regexp
}

// Route is a route computed from facts. When and Port are expressions.
type Route struct {
	ID    string      `yaml:"id"`
	When  string      `yaml:"when"`
	Port  string      `yaml:"port"`
	Label string      `yaml:"label"`
	Role  config.Role `yaml:"role"`
	Web   WebHint     `yaml:"web"`
}

// Advisory is advice shown when When holds. Message and Snippet may hold
// {{ expression }} parts.
type Advisory struct {
	ID      string `yaml:"id"`
	When    string `yaml:"when"`
	Message string `yaml:"message"`
	Snippet string `yaml:"snippet"`
}

var (
	nameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	varRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	factRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// ValidName reports whether s can name a preset.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// validRef reports whether s can refer to another preset: a plain name, or
// <alias>/<name> for a local preset that waits on a registry preset.
func validRef(s string) bool {
	alias, name, qualified := strings.Cut(s, "/")
	if !qualified {
		return ValidName(s)
	}
	return ValidName(alias) && ValidName(name)
}

// Parse reads one preset. name is the file name without ".yaml", and the
// preset must carry the same name. Every problem found is reported.
func Parse(name string, data []byte) (*Preset, error) {
	file := name + ".yaml"
	var head struct {
		Schema int `yaml:"schema"`
	}
	if err := yaml.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if head.Schema > SchemaVersion {
		return nil, fmt.Errorf("%s: schema %d, and this nearhost understands up to %d; upgrade nearhost: %w", file, head.Schema, SchemaVersion, ErrNewerSchema)
	}
	var p Preset
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&p); errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: the file is empty", file)
	} else if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if errs := p.validate(name); len(errs) > 0 {
		return nil, fmt.Errorf("%s: %w", file, errors.Join(errs...))
	}
	return &p, nil
}

// validate checks the preset and fills in its defaults.
func (p *Preset) validate(name string) []error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if p.Schema != SchemaVersion {
		bad("schema must be %d, not %d", SchemaVersion, p.Schema)
	}
	if !ValidName(p.Name) {
		bad("name %q must be lowercase letters, digits and hyphens", p.Name)
	} else if p.Name != name {
		bad("name %q must match the file name %q", p.Name, name)
	}
	if strings.TrimSpace(p.Title) == "" {
		bad("title is required")
	}
	checkText("title", p.Title, "", bad)
	checkText("description", p.Description, "", bad)
	if strings.TrimSpace(p.Detect) == "" {
		bad("detect is required")
	}
	switch p.Compose {
	case "", ComposePorts:
	default:
		bad("compose %q must be %s", p.Compose, ComposePorts)
	}
	for _, a := range p.After {
		if !validRef(a) || a == p.Name {
			bad("after: %q is not another preset's name", a)
		}
	}

	ids := map[string]bool{}
	claim := func(at, id string) {
		if ids[id] {
			bad("%s: id %q is used twice", at, id)
		}
		ids[id] = true
	}
	for i := range p.Ports {
		e := &p.Ports[i]
		at := fmt.Sprintf("ports[%d]", i)
		if !varRe.MatchString(e.Var) {
			bad("%s: var %q is not a variable name", at, e.Var)
		} else if ids[e.Var] {
			bad("%s: var %q is listed twice", at, e.Var)
		}
		ids[e.Var] = true
		if strings.TrimSpace(e.Label) == "" {
			bad("%s: label is required", at)
		}
		checkText(at+": label", e.Label, "", bad)
		checkRole(&e.Role, at, bad)
		checkWeb(e.Web, at, bad)
	}
	for _, fname := range slices.Sorted(maps.Keys(p.Facts)) {
		f := p.Facts[fname]
		at := "facts." + fname
		if !factRe.MatchString(fname) {
			bad("%s: a fact name must be lowercase letters, digits and underscores", at)
		}
		if len(f.Scan) == 0 {
			bad("%s: scan needs at least one file", at)
		}
		for _, file := range f.Scan {
			if err := checkPath(file); err != nil {
				bad("%s: %w", at, err)
			}
		}
		if f.Regex == "" {
			bad("%s: regex is required", at)
		} else if re, err := regexp.Compile(f.Regex); err != nil {
			bad("%s: regex: %w", at, err)
		} else {
			f.re = re
		}
		switch f.As {
		case "bool":
		case "int", "string", "quoted_list":
			if f.re != nil && f.re.NumSubexp() != 1 {
				bad("%s: as %s needs exactly one capture group, and the regex has %d", at, f.As, f.re.NumSubexp())
			}
		default:
			bad("%s: as %q must be int, string, bool or quoted_list", at, f.As)
		}
		p.Facts[fname] = f
	}
	for i := range p.Routes {
		r := &p.Routes[i]
		at := fmt.Sprintf("routes[%d]", i)
		if !ValidName(r.ID) {
			bad("%s: id %q must be lowercase letters, digits and hyphens", at, r.ID)
		} else {
			claim(at, r.ID)
		}
		if strings.TrimSpace(r.Port) == "" {
			bad("%s: port is required", at)
		}
		if strings.TrimSpace(r.Label) == "" {
			bad("%s: label is required", at)
		}
		checkText(at+": label", r.Label, "", bad)
		checkRole(&r.Role, at, bad)
		checkWeb(r.Web, at, bad)
	}
	for i, a := range p.Advisories {
		at := fmt.Sprintf("advisories[%d]", i)
		if !ValidName(a.ID) {
			bad("%s: id %q must be lowercase letters, digits and hyphens", at, a.ID)
		} else {
			claim(at, a.ID)
		}
		if strings.TrimSpace(a.When) == "" {
			bad("%s: when is required", at)
		}
		if strings.TrimSpace(a.Message) == "" {
			bad("%s: message is required", at)
		}
		checkText(at+": message", a.Message, "\n\t", bad)
		checkText(at+": snippet", a.Snippet, "\n\t", bad)
	}
	for i, id := range p.Suppress {
		if strings.TrimSpace(id) == "" {
			bad("suppress[%d]: an id is required", i)
		}
	}
	p.checkExpressions(bad)
	return errs
}

// checkText refuses control characters, other than those in allow, in text
// that is printed or saved. A template is checked as written.
func checkText(field, text, allow string, bad func(string, ...any)) {
	if strings.IndexFunc(text, func(r rune) bool { return unicode.IsControl(r) && !strings.ContainsRune(allow, r) }) >= 0 {
		bad("%s must not contain control characters", field)
	}
}

func checkRole(role *config.Role, at string, bad func(string, ...any)) {
	switch *role {
	case "":
		*role = config.RoleIgnore
	case config.RoleIgnore, config.RoleLoopback:
	case config.RoleWeb:
		bad("%s: a preset cannot set role web; use `web: preferred` or `web: fallback`", at)
	default:
		bad("%s: role %q must be ignore or loopback", at, *role)
	}
}

func checkWeb(hint WebHint, at string, bad func(string, ...any)) {
	switch hint {
	case WebNone, WebPreferred, WebFallback:
	default:
		bad("%s: web %q must be preferred or fallback", at, hint)
	}
}

// checkPath refuses anything but a plain file path inside the project.
func checkPath(name string) error {
	switch {
	case name == "":
		return errors.New("a file name is empty")
	case strings.ContainsAny(name, `*?[\`):
		return fmt.Errorf("file %q: globs and backslashes are not allowed", name)
	case path.IsAbs(name):
		return fmt.Errorf("file %q must be relative to the project", name)
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return fmt.Errorf("file %q must stay inside the project", name)
		}
	}
	return nil
}
