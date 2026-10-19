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
	"github.com/nearhost/nearhost/internal/hostname"
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
	Routes []Route
	// SHA256 is the hex hash of the canonical JSON of Routes after filling.
	// Hashing the filled values rather than the file means a change in .env
	// that moves a port or host invalidates an earlier approval, while
	// comments and formatting do not.
	SHA256 string
}

// varRef matches the braced forms discovery.Expand understands, on one line,
// so a stray "${" in a comment cannot swallow the YAML after it.
var varRef = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*(?::?-[^}\n]*)?\}`)

var unknownField = regexp.MustCompile(`^line (\d+): field (\S+) not found in type`)

func newerVersion(v int) error {
	return fmt.Errorf("%s is version %d; this Nearhost understands version %d, so update Nearhost", FileName, v, Version)
}

func token(i int) string { return "nhrcvar" + strconv.Itoa(i) + "x" }

type rawFile struct {
	Version int        `yaml:"version"`
	Routes  []rawRoute `yaml:"routes"`
}

type rawRoute struct {
	Port  yaml.Node   `yaml:"port"`
	Hosts []string    `yaml:"hosts"`
	Label string      `yaml:"label"`
	Role  config.Role `yaml:"role"`
}

// Parse decodes data and fills ${VAR} references in ports and hosts from env.
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
	for _, rr := range raw.Routes {
		r, err := fill(rr, env, restore)
		if err != nil {
			return nil, err
		}
		routes = append(routes, r)
	}

	if err := checkRoutes(routes); err != nil {
		return nil, err
	}

	canon, err := json.Marshal(routes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	sum := sha256.Sum256(canon)
	return &File{Routes: routes, SHA256: hex.EncodeToString(sum[:])}, nil
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
			return fmt.Errorf("%s: port %d needs at least one host; only the first web route is served at the project's .test name", FileName, r.Port)
		}
		for _, h := range r.Hosts {
			k := strings.ToLower(h)
			if hosts[k] {
				return fmt.Errorf("%s: host %q (port %d) is listed twice", FileName, h, r.Port)
			}
			hosts[k] = true
		}
	}
	if webs == 0 {
		return fmt.Errorf("%s needs at least one web route", FileName)
	}
	return nil
}

// fill turns one raw route into a Route, filling and checking its values.
func fill(rr rawRoute, env map[string]string, restore *strings.Replacer) (Route, error) {
	// Labels and roles go through the same token swap as ports and hosts,
	// so put their references back and fill them too.
	label, name, ok := discovery.Expand(restore.Replace(rr.Label), env)
	if !ok {
		return Route{}, unset(name)
	}
	role, name, ok := discovery.Expand(restore.Replace(string(rr.Role)), env)
	if !ok {
		return Route{}, unset(name)
	}
	r := Route{Role: config.Role(role), Label: label}
	if r.Role == "" {
		r.Role = config.RoleWeb
	}
	if !r.Role.Valid() {
		return r, fmt.Errorf("%s: role %q must be web, loopback or ignore", FileName, r.Role)
	}

	value, varName, ok := discovery.Expand(restore.Replace(rr.Port.Value), env)
	if !ok {
		return r, unset(varName)
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		if varName != "" {
			return r, fmt.Errorf("%s: port %q from variable %q is not a number from 1 to 65535", FileName, value, varName)
		}
		return r, fmt.Errorf("%s: port %q is not a number from 1 to 65535", FileName, value)
	}
	r.Port = port

	if len(rr.Hosts) > 0 && r.Role != config.RoleWeb {
		return r, fmt.Errorf("%s: port %d: only web routes have hosts", FileName, port)
	}
	for _, h := range rr.Hosts {
		host, name, ok := discovery.Expand(restore.Replace(h), env)
		if !ok {
			return r, unset(name)
		}
		if err := hostname.Valid(host); err != nil {
			return r, fmt.Errorf("%s: %w", FileName, err)
		}
		r.Hosts = append(r.Hosts, host)
	}
	return r, nil
}

func unset(name string) error {
	return fmt.Errorf("%s: ${%s} has no value in .env.local or .env, and no default", FileName, name)
}

// Read reads .nearhostrc and the project's env through r. A project without
// the file is normal, so that gives found == false and no error.
func Read(r discovery.FileReader) (f *File, found bool, err error) {
	data, err := r.ReadFile(FileName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", FileName, err)
	}
	env, err := discovery.ReadEnv(r)
	if err != nil {
		return nil, true, err
	}
	f, err = Parse(data, env)
	return f, true, err
}
