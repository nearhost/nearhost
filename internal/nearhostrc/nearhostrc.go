// Package nearhostrc reads a project's committed .nearhostrc file: the web
// routes, ports and hosts the project asks Nearhost to serve. The file is
// shared through git, so it can be written by someone else; this package only
// parses and fills it, and leaves the decision to trust it to the caller.
package nearhostrc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/safetext"
)

const (
	// FileName is the file's name in the project root.
	FileName = ".nearhostrc"
	// Version is the only format version this Nearhost understands.
	Version = 1
)

// Route is one port the project asks for, with its variables already filled.
type Route struct {
	Port  int         `json:"port"`
	Role  config.Role `json:"role"`
	Label string      `json:"label,omitempty"`
	Hosts []string    `json:"hosts,omitempty"`
}

// File is a parsed .nearhostrc.
type File struct {
	// Routes are what the project serves, when the file says; a file
	// without routes, as one that only sets the alias, leaves them to
	// discovery and the user.
	Routes []Route
	// DockerClients is whether the file asks for the project to be served to
	// containers on local Docker networks.
	DockerClients bool
	// Alias is the project's main name the file asks for, filled and
	// checked as config.ValidProjectAlias does; empty when the file has none.
	Alias string
	// SHA256 is the hex hash of the canonical JSON of Routes, and of the
	// options and alias when the file has them, after filling. Hashing the
	// filled values rather than the file means a change in .env that moves a
	// port, host or main name invalidates an earlier approval, while comments
	// and formatting do not.
	SHA256 string
}

// varRef matches the braced forms discovery.ExpandBraced understands, on one
// line, so a stray "${" in a comment cannot swallow the YAML after it.
var varRef = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*(?::?-[^}\n]*)?\}`)

var unknownField = regexp.MustCompile(`^line (\d+): field (\S+) not found in type`)

// VersionError is the error for a .nearhostrc whose version is newer than this
// Nearhost understands. It is a type so that a caller can tell it from a
// malformed file: the cure is to update Nearhost, not to edit the file.
type VersionError struct{ Version int }

func (e *VersionError) Error() string {
	return fmt.Sprintf("%s is version %d; this Nearhost understands version %d, so update Nearhost", FileName, e.Version, Version)
}

func newerVersion(v int) error { return &VersionError{Version: v} }

func token(i int) string { return "nhrcvar" + strconv.Itoa(i) + "x" }

type rawFile struct {
	Version int        `yaml:"version"`
	Routes  []rawRoute `yaml:"routes"`

	DockerClients bool   `yaml:"docker_clients"`
	Alias         string `yaml:"alias"`
}

type rawRoute struct {
	Port  yaml.Node   `yaml:"port"`
	Hosts []string    `yaml:"hosts"`
	Label string      `yaml:"label"`
	Role  config.Role `yaml:"role"`
}

// Parse decodes data and fills ${VAR} references in ports, hosts, labels,
// roles and the alias from env. Labels lose their control characters.
// Every error starts with ".nearhostrc" so callers can show it as it is.
func Parse(data []byte, env map[string]string) (*File, error) {
	// Editors on Windows add a byte order mark that YAML would read as text.
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))

	// A "}" ends a plain scalar inside a YAML flow list, so hosts: [a.${D}]
	// would not parse. Swap each reference for a plain token while decoding
	// and put it back afterwards.
	var refs []string
	data = varRef.ReplaceAllFunc(data, func(m []byte) []byte {
		refs = append(refs, string(m))
		return []byte(token(len(refs) - 1))
	})
	pairs := make([]string, 0, 2*len(refs))
	for i, ref := range refs {
		pairs = append(pairs, token(i), ref)
	}
	restore := strings.NewReplacer(pairs...)

	var raw rawFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		// An empty or comments-only file has no YAML document at all, so
		// Decode says EOF; what the file is missing is its version line.
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s needs version: %d", FileName, Version)
		}
		var te *yaml.TypeError
		if !errors.As(err, &te) {
			return nil, yamlError(err, restore)
		}
		// A newer file may use keys this build lacks, so say the version
		// first; yaml still fills the fields it knows.
		if raw.Version > Version {
			return nil, newerVersion(raw.Version)
		}
		for _, msg := range te.Errors {
			if m := unknownField.FindStringSubmatch(msg); m != nil {
				return nil, fmt.Errorf("%s: line %s: key %q is not understood by this version of Nearhost; update Nearhost, or remove it", FileName, m[1], m[2])
			}
		}
		return nil, yamlError(err, restore)
	}
	switch {
	case raw.Version == 0:
		return nil, fmt.Errorf("%s needs version: %d", FileName, Version)
	case raw.Version > Version:
		return nil, newerVersion(raw.Version)
	case raw.Version != Version:
		return nil, fmt.Errorf("%s: version %d is not valid", FileName, raw.Version)
	}

	routes := make([]Route, 0, len(raw.Routes))
	at := fieldLines(data)
	for i, rr := range raw.Routes {
		r, err := fill(rr, env, restore, func(field string, host int) int { return at(i, field, host) })
		if err != nil {
			return nil, err
		}
		routes = append(routes, r)
	}

	if err := checkRoutes(routes); err != nil {
		return nil, err
	}

	// The alias goes through the same token swap as the labels.
	alias, name, ok := discovery.ExpandBraced(restore.Replace(raw.Alias), env)
	if !ok {
		return nil, unset(name, at(-1, "alias", 0))
	}
	if alias != "" {
		if err := config.ValidProjectAlias(alias); err != nil {
			return nil, bareHint(fmt.Errorf("%s: alias: %w", FileName, err), restore.Replace(raw.Alias), at(-1, "alias", 0))
		}
	}

	// Without the option or an alias the hash stays the plain routes, as
	// before, so approvals given by earlier versions are not invalidated by
	// an upgrade. The option is added to the hashed form only when it is on,
	// and the alias only when there is one, so a file with only the option
	// hashes as it did before alias existed, and adding either asks again.
	var canon []byte
	var err error
	if raw.DockerClients || alias != "" {
		canon, err = json.Marshal(struct {
			Routes        []Route `json:"routes"`
			DockerClients bool    `json:"docker_clients"`
			Alias         string  `json:"alias,omitempty"`
		}{routes, raw.DockerClients, alias})
	} else {
		canon, err = json.Marshal(routes)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	sum := sha256.Sum256(canon)
	return &File{Routes: routes, DockerClients: raw.DockerClients, Alias: alias, SHA256: hex.EncodeToString(sum[:])}, nil
}

// yamlError wraps a decoding error, putting back the ${VAR} references Parse
// swapped for tokens, so the message quotes what the user wrote.
func yamlError(err error, restore *strings.Replacer) error {
	return fmt.Errorf("%s: %s", FileName, restore.Replace(err.Error()))
}

// checkRoutes rejects routes that could never become a valid project, so the
// user hears about the file's mistake instead of a vaguer error from the
// configuration it is merged into.
func checkRoutes(routes []Route) error {
	ports := map[int]bool{}
	hosts := map[string]bool{}
	webs := 0
	for _, r := range routes {
		if ports[r.Port] {
			return fmt.Errorf("%s: port %d is listed twice", FileName, r.Port)
		}
		ports[r.Port] = true
		if r.Role != config.RoleWeb {
			continue
		}
		webs++
		if webs > 1 && len(r.Hosts) == 0 {
			return fmt.Errorf("%s: port %d needs at least one host; only the first web route is served at the project's main name", FileName, r.Port)
		}
		for _, h := range r.Hosts {
			k := strings.ToLower(h)
			if hosts[k] {
				return fmt.Errorf("%s: host %q (port %d) is listed twice", FileName, h, r.Port)
			}
			hosts[k] = true
		}
	}
	if webs == 0 && len(routes) > 0 {
		return fmt.Errorf("%s needs at least one web route among its routes", FileName)
	}
	return nil
}

// fill turns one raw route into a Route, filling and checking its values.
func fill(rr rawRoute, env map[string]string, restore *strings.Replacer, at func(field string, host int) int) (Route, error) {
	// Labels and roles go through the same token swap as ports and hosts,
	// so put their references back and fill them too.
	label, name, ok := discovery.ExpandBraced(restore.Replace(rr.Label), env)
	if !ok {
		return Route{}, unset(name, at("label", 0))
	}
	role, name, ok := discovery.ExpandBraced(restore.Replace(string(rr.Role)), env)
	if !ok {
		return Route{}, unset(name, at("role", 0))
	}
	// A label is printed and saved, and a double-quoted YAML string or a
	// variable can give it escape sequences, so it loses its control
	// characters before it is hashed. A label without any is left as it is,
	// so the hash of a file approved before stays the same; one that had
	// some is also trimmed, as a line break at its end would leave a space.
	if clean := safetext.Clean(label); clean != label {
		label = strings.TrimSpace(clean)
	}
	r := Route{Role: config.Role(role), Label: label}
	if r.Role == "" {
		r.Role = config.RoleWeb
	}
	if !r.Role.Valid() {
		return r, bareHint(fmt.Errorf("%s: role %q must be web, loopback or ignore", FileName, r.Role), restore.Replace(string(rr.Role)), at("role", 0))
	}

	value, varName, ok := discovery.ExpandBraced(restore.Replace(rr.Port.Value), env)
	if !ok {
		return r, unset(varName, at("port", 0))
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		if varName != "" {
			return r, fmt.Errorf("%s: port %q from variable %q is not a number from 1 to 65535", FileName, value, varName)
		}
		return r, bareHint(fmt.Errorf("%s: port %q is not a number from 1 to 65535", FileName, value), restore.Replace(rr.Port.Value), at("port", 0))
	}
	r.Port = port

	if len(rr.Hosts) > 0 && r.Role != config.RoleWeb {
		return r, fmt.Errorf("%s: port %d: only web routes have hosts", FileName, port)
	}
	for i, h := range rr.Hosts {
		host, name, ok := discovery.ExpandBraced(restore.Replace(h), env)
		if !ok {
			return r, unset(name, at("hosts", i))
		}
		if err := config.ValidProjectHost(host); err != nil {
			return r, bareHint(fmt.Errorf("%s: %w", FileName, err), restore.Replace(h), at("hosts", i))
		}
		r.Hosts = append(r.Hosts, host)
	}
	return r, nil
}

// UnsetError is the error for a ${VAR} in a .nearhostrc that has no value in
// .env.local or .env and no default. It is a type so that a caller can name
// the variable. Line is the 1-based line of the value that uses it, or 0.
type UnsetError struct {
	Var  string
	Line int
}

func (e *UnsetError) Error() string {
	return fmt.Sprintf("%s: ${%s} has no value in .env.local or .env, and no default", at(e.Line), e.Var)
}

func unset(name string, line int) error { return &UnsetError{Var: name, Line: line} }

// at is the start of an error message: the file name, and the line when it
// is known.
func at(line int) string {
	if line > 0 {
		return fmt.Sprintf("%s line %d", FileName, line)
	}
	return FileName
}

// bareVar matches a "$" followed by an identifier, the unbraced form a
// .nearhostrc no longer fills.
var bareVar = regexp.MustCompile(`\$([A-Za-z_][A-Za-z0-9_]*)`)

// BareVarError is a validation error for a value written with a bare $NAME.
// Older versions filled it, so the error says to write ${NAME}. Line is the
// 1-based line of the value, or 0.
type BareVarError struct {
	Err  error
	Var  string
	Line int
}

func (e *BareVarError) Error() string {
	return fmt.Sprintf("%s: %s. Write a variable as ${%s}.", at(e.Line), strings.TrimPrefix(e.Err.Error(), FileName+": "), e.Var)
}

func (e *BareVarError) Unwrap() error { return e.Err }

// bareHint returns err as a BareVarError when written, the value as it stands
// in the file with its braced references still in place, holds a bare $NAME,
// and err itself when it does not. A braced reference and its default are
// left out of the search, so a value from .env that holds a "$" never gets the
// hint.
func bareHint(err error, written string, line int) error {
	if m := bareVar.FindStringSubmatch(varRef.ReplaceAllString(written, "")); m != nil {
		return &BareVarError{Err: err, Var: m[1], Line: line}
	}
	return err
}

// fieldLines decodes data again, without checking its keys, and returns a
// function that gives the 1-based line of a field: route is the route's index
// (or -1 for the file's own fields), field one of label, role, port, hosts or
// alias, and host the index into hosts. It gives 0 when the field is not
// there. The decode runs on the first call only, which is on the error path.
func fieldLines(data []byte) func(route int, field string, host int) int {
	var lines struct {
		Routes []struct {
			Port  yaml.Node   `yaml:"port"`
			Hosts []yaml.Node `yaml:"hosts"`
			Label yaml.Node   `yaml:"label"`
			Role  yaml.Node   `yaml:"role"`
		} `yaml:"routes"`
		Alias yaml.Node `yaml:"alias"`
	}
	done := false
	return func(route int, field string, host int) int {
		if !done {
			done = true
			_ = yaml.Unmarshal(data, &lines)
		}
		if route < 0 {
			return lines.Alias.Line
		}
		if route >= len(lines.Routes) {
			return 0
		}
		r := lines.Routes[route]
		switch field {
		case "label":
			return r.Label.Line
		case "role":
			return r.Role.Line
		case "port":
			return r.Port.Line
		case "hosts":
			if host < len(r.Hosts) {
				return r.Hosts[host].Line
			}
		}
		return 0
	}
}

var errLine = regexp.MustCompile(`\bline (\d+):`)

// Line returns the 1-based line that err, as Parse or Read returned it,
// points at, or 0 when it points at none. It is the number in the first
// "line N:" of the message, which an unset variable, a bare $NAME and a
// problem in the YAML all carry, so a message that has been turned into text
// still gives its line. The data argument is not read.
func Line(_ []byte, err error) int {
	if err == nil {
		return 0
	}
	if m := errLine.FindStringSubmatch(err.Error()); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// Read reads .nearhostrc and the project's env through r. A project without
// the file is normal, so that gives found == false and no error.
func Read(r discovery.FileReader) (f *File, found bool, err error) {
	data, err := r.ReadFile(FileName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, discovery.ReadError(FileName, err)
	}
	env, err := discovery.ReadEnv(r)
	if err != nil {
		return nil, true, err
	}
	f, err = Parse(data, env)
	return f, true, err
}
