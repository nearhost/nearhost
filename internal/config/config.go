// Package config models and persists the nearhost configuration file.
package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/nearhost/nearhost/internal/hostname"
	"github.com/nearhost/nearhost/internal/report"
	"gopkg.in/yaml.v3"
)

// Role says what nearhost does with a discovered port.
type Role string

const (
	// RoleWeb is served over HTTPS at its hosts. The first is served at the
	// alias as well.
	RoleWeb Role = "web"
	// RoleLoopback is forwarded to 127.0.0.1:<port> on this machine.
	RoleLoopback Role = "loopback"
	// RoleIgnore is listed but not forwarded.
	RoleIgnore Role = "ignore"
)

// Valid reports whether r is one of the known roles.
func (r Role) Valid() bool {
	return r == RoleWeb || r == RoleLoopback || r == RoleIgnore
}

// LocalServer is the implicit server for projects on this machine.
const LocalServer = "local"

// Route is one port on the project's server and what to do with it.
type Route struct {
	Port  int    `yaml:"port"`
	Role  Role   `yaml:"role"`
	Label string `yaml:"label,omitempty"`
	// Hosts are the exact hosts and wildcard patterns a web route is served
	// at, besides the alias on the first one. Only web routes have them.
	Hosts []string `yaml:"hosts,omitempty"`
}

// Project is one site nearhost can serve.
type Project struct {
	Name    string  `yaml:"name"`
	Server  string  `yaml:"server"`
	Path    string  `yaml:"path"`
	Alias   string  `yaml:"alias"`
	Enabled bool    `yaml:"enabled"`
	Routes  []Route `yaml:"routes"`
	// Nearhostrc is what was last approved from the project's .nearhostrc,
	// or nil when the project doesn't use one. Keeping it lets a changed
	// file be told apart from hosts added by hand.
	Nearhostrc *RcSnapshot `yaml:"nearhostrc,omitempty"`
}

// RcSnapshot is the content of a project's .nearhostrc at the time the user
// approved it.
type RcSnapshot struct {
	SHA256 string  `yaml:"sha256"`
	Routes []Route `yaml:"routes"`
	// DockerClients is whether the approved file asked for the project to be
	// served to containers on local Docker networks.
	DockerClients bool `yaml:"docker_clients,omitempty"`
	// Alias is the main name the approved file asked for; empty when it
	// asked for none. It is what tells a main name the file moved from one
	// the user set by hand, which a later read of the same file keeps.
	Alias string `yaml:"alias,omitempty"`
}

// Clone returns a deep copy of s, so a caller can hold or change it without
// touching the original's routes or hosts. A nil snapshot clones to nil.
func (s *RcSnapshot) Clone() *RcSnapshot {
	if s == nil {
		return nil
	}
	c := *s
	c.Routes = make([]Route, len(s.Routes))
	for i, r := range s.Routes {
		r.Hosts = append([]string(nil), r.Hosts...)
		c.Routes[i] = r
	}
	return &c
}

// DockerClients reports whether the project's approved .nearhostrc asks to be
// served to containers on local Docker networks.
func (p Project) DockerClients() bool {
	return p.Nearhostrc != nil && p.Nearhostrc.DockerClients
}

// WebRoute returns the project's primary web route, the first one, if present.
func (p Project) WebRoute() (Route, bool) {
	for _, r := range p.Routes {
		if r.Role == RoleWeb {
			return r, true
		}
	}
	return Route{}, false
}

// WebHosts is one web route and every host it is served at.
type WebHosts struct {
	Port  int
	Label string
	Hosts []string
}

// Web lists the project's web routes in route order with their hosts. The
// primary route is also served at the alias, so the alias comes first among
// its hosts, and a copy of it written among them is dropped.
func (p Project) Web() []WebHosts {
	var out []WebHosts
	for _, r := range p.Routes {
		if r.Role != RoleWeb {
			continue
		}
		w := WebHosts{Port: r.Port, Label: r.Label}
		if len(out) == 0 {
			w.Hosts = append(w.Hosts, p.Alias)
		}
		for _, h := range r.Hosts {
			if len(out) == 0 && strings.EqualFold(h, p.Alias) {
				continue
			}
			w.Hosts = append(w.Hosts, h)
		}
		out = append(out, w)
	}
	return out
}

// AllHosts lists every host the project's web routes are served at, in order.
func (p Project) AllHosts() []string {
	var out []string
	for _, w := range p.Web() {
		out = append(out, w.Hosts...)
	}
	return out
}

// HostClash finds the first host of p that another enabled project also
// serves, and returns the name the two contest with that project's name. Two
// exact hosts clash when equal and two patterns when written the same,
// ignoring case. An exact host under another project's pattern clashes too,
// in either direction: the proxy sends the exact host to its own project, so
// that project would take requests, and cookies set for the pattern's
// domain, meant for the other. The contested name is then the exact host.
// The project in c named like p is skipped, so a stored project can be
// checked against its own config, and a pattern and an exact host of one
// project never clash. Callers check it before a change is saved; Validate
// does not, so a config that already holds such a pair still loads, and
// doctor warns about it.
func (c *Config) HostClash(p Project) (host, other string, clash bool) {
	hc, ok := c.HostConflict(p)
	if !ok {
		return "", "", false
	}
	host = hc.Host
	if hostname.IsPattern(hc.Host) && !hostname.IsPattern(hc.OtherHost) {
		host = hc.OtherHost
	}
	return host, hc.Other, true
}

// HostConflict is a host of one project that another enabled project
// claims: the same name, or an exact host and a pattern that matches it.
type HostConflict struct {
	Project, Host    string // the project checked and its host
	Other, OtherHost string // the project that claims Host, and how
}

// Error says which projects claim which name and what to do about it.
func (h HostConflict) Error() string {
	switch exact, otherExact := !hostname.IsPattern(h.Host), !hostname.IsPattern(h.OtherHost); {
	case exact && !otherExact:
		return fmt.Sprintf("host %s of project %q falls under %s, which project %q serves, so %q would take requests and cookies meant for %q; serve %s from %q, or remove it from %q",
			h.Host, h.Project, h.OtherHost, h.Other, h.Project, h.Other, h.Host, h.Other, h.Project)
	case !exact && otherExact:
		return fmt.Sprintf("pattern %s of project %q covers %s, which project %q serves, so %q would take requests and cookies meant for %q; remove %s from %q, or give %q a pattern that does not cover it",
			h.Host, h.Project, h.OtherHost, h.Other, h.Other, h.Project, h.OtherHost, h.Other, h.Project)
	}
	return fmt.Sprintf("host %s is already served by project %q", h.Host, h.Other)
}

// HostConflict is HostClash with both sides of the clash.
func (c *Config) HostConflict(p Project) (HostConflict, bool) {
	return c.HostConflictSince(nil, p)
}

// HostConflictSince is HostConflict for a change to c from before, the
// projects as they were: it finds a clash p brings in that before did not
// have. An exact host under another project's pattern that two enabled
// projects of before already had is let through, so a pair saved before such
// pairs were refused does not block other changes, renames included; doctor
// warns about it. Two equal hosts are refused whether before had them or not,
// as they always were. With no before it is HostConflict.
func (c *Config) HostConflictSince(before []Project, p Project) (HostConflict, bool) {
	had := patternPairs((&Config{Projects: before}).PatternConflicts())
	for _, h := range p.AllHosts() {
		for _, q := range c.Projects {
			if q.Name == p.Name || !q.Enabled {
				continue
			}
			for _, qh := range q.AllHosts() {
				if !hostsClash(h, qh) {
					continue
				}
				hc := HostConflict{Project: p.Name, Host: h, Other: q.Name, OtherHost: qh}
				if !strings.EqualFold(h, qh) && had[hc.pair()] {
					continue
				}
				return hc, true
			}
		}
	}
	return HostConflict{}, false
}

// pair is the exact host and the pattern of a clash between the two, lower
// case, whichever project each belongs to.
func (h HostConflict) pair() [2]string {
	if hostname.IsPattern(h.Host) {
		return [2]string{strings.ToLower(h.OtherHost), strings.ToLower(h.Host)}
	}
	return [2]string{strings.ToLower(h.Host), strings.ToLower(h.OtherHost)}
}

// patternPairs is the set of the pairs of conflicts.
func patternPairs(conflicts []HostConflict) map[[2]string]bool {
	set := map[[2]string]bool{}
	for _, hc := range conflicts {
		set[hc.pair()] = true
	}
	return set
}

// ClaimedBy finds the first enabled project other than the one named name
// that claims host, as HostClash has it, and the host it claims it with.
func (c *Config) ClaimedBy(name, host string) (other, otherHost string, ok bool) {
	for _, q := range c.Projects {
		if q.Name == name || !q.Enabled {
			continue
		}
		for _, qh := range q.AllHosts() {
			if hostsClash(host, qh) {
				return q.Name, qh, true
			}
		}
	}
	return "", "", false
}

// PatternConflicts lists each exact host of an enabled project with each
// pattern of another enabled project that matches it, in project and host
// order, the exact host's project first. A config saved before such a pair was
// refused may hold one; the proxy sends the exact host to its own project.
func (c *Config) PatternConflicts() []HostConflict {
	var out []HostConflict
	for _, p := range c.Projects {
		if !p.Enabled {
			continue
		}
		for _, h := range p.AllHosts() {
			if hostname.IsPattern(h) {
				continue
			}
			for _, q := range c.Projects {
				if q.Name == p.Name || !q.Enabled {
					continue
				}
				for _, qh := range q.AllHosts() {
					if hostname.IsPattern(qh) && hostname.Match(strings.ToLower(qh), h) {
						out = append(out, HostConflict{Project: p.Name, Host: h, Other: q.Name, OtherHost: qh})
					}
				}
			}
		}
	}
	return out
}

// hostsClash reports whether two projects serving a and b would both claim
// a name: a and b are equal ignoring case, or one is a pattern that matches
// the other, an exact host.
func hostsClash(a, b string) bool {
	if strings.EqualFold(a, b) {
		return true
	}
	switch pa, pb := hostname.IsPattern(a), hostname.IsPattern(b); {
	case pa && !pb:
		return hostname.Match(strings.ToLower(a), b)
	case pb && !pa:
		return hostname.Match(strings.ToLower(b), a)
	}
	return false
}

// LoopbackOnOwnPort finds the first loopback route of p whose port is one of
// own, the ports Nearhost itself listens on on this machine. A 0 in own
// stands for a port Nearhost does not serve and matches nothing.
func (p Project) LoopbackOnOwnPort(own ...int) (int, bool) {
	for _, r := range p.Routes {
		if r.Role == RoleLoopback && r.Port != 0 && slices.Contains(own, r.Port) {
			return r.Port, true
		}
	}
	return 0, false
}

// ValidProjectAlias reports why alias cannot be a project's main name, or
// returns nil. The main name is one exact, lowercase domain: a .test name or a
// real one. A wildcard pattern would stand for many names, and localhost and
// an IP address are not names a hosts file or the proxy's certificate can
// serve as a project's own.
func ValidProjectAlias(alias string) error {
	if strings.EqualFold(alias, hostname.PageHost) {
		return errPageHost
	}
	if _, err := netip.ParseAddr(alias); err == nil || alias == "localhost" ||
		hostname.Valid(alias) != nil || hostname.IsPattern(alias) || alias == ".test" {
		return fmt.Errorf("invalid main name %q: must be a lowercase domain name, not a wildcard, localhost or an IP address", alias)
	}
	return nil
}

// errPageHost is the error for a project that claims Nearhost's own page.
var errPageHost = errors.New(hostname.PageHost + " is Nearhost's own page")

// ValidProjectHost reports why h cannot be a host of a project's web route,
// or returns nil: it must be a valid host or pattern (see hostname.Valid),
// and not Nearhost's own page in any case, which only Nearhost serves and
// gives its cookie at.
func ValidProjectHost(h string) error {
	if strings.EqualFold(h, hostname.PageHost) {
		return errPageHost
	}
	return hostname.Valid(h)
}

// HostsFileNames lists the names nearhost puts in the hosts file for this
// config before any DNS filtering: the main names of projects and the exact
// hosts of the web routes of enabled ones, sorted and without duplicates. A
// .test main name is listed for every project, as no DNS serves it. A real
// domain, whether a main name or a route host, is listed only for an enabled
// project, because it also has a site elsewhere and an entry here would hide
// that site for a project nothing serves. localhost and wildcard patterns are
// left out, as a hosts file resolves neither, and so is an invalid main name,
// which no project is started with. It is the single candidate list the
// engine, doctor and the interface share, so they cannot disagree about what
// the file should hold.
func (c *Config) HostsFileNames() []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	for _, p := range c.Projects {
		if c.UnderNetworkDomain(p.Alias) {
			continue
		}
		if ValidProjectAlias(p.Alias) == nil && (p.Enabled || strings.HasSuffix(p.Alias, ".test")) {
			add(p.Alias)
		}
		if !p.Enabled {
			continue
		}
		for _, r := range p.Routes {
			if r.Role != RoleWeb {
				continue
			}
			for _, h := range r.Hosts {
				h = strings.ToLower(h)
				if h != "localhost" && !hostname.IsPattern(h) && !c.UnderNetworkDomain(h) {
					add(h)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// Server is a machine reachable over SSH.
type Server struct {
	Name    string `yaml:"name"`
	SSHHost string `yaml:"ssh_host"`
}

// Registry is a preset registry the user trusts: a public GitHub repository
// and the alias its presets are named under.
type Registry struct {
	Alias string `yaml:"alias"`
	Repo  string `yaml:"repo"` // owner/name
}

// PresetRef is an enabled preset. A preset from a registry is named
// <alias>/<name> and has Ref and SHA256 set. A local file has a plain name.
type PresetRef struct {
	Name   string `yaml:"name"`
	Ref    string `yaml:"ref,omitempty"`
	SHA256 string `yaml:"sha256,omitempty"`
}

// Config is the whole configuration file.
type Config struct {
	Servers  []Server  `yaml:"servers"`
	Projects []Project `yaml:"projects"`
	// Registries are the preset registries the user trusts. With none, no
	// request for presets is ever made.
	Registries []Registry `yaml:"registries,omitempty"`
	// RegistryPrompt false stops `add` asking whether to trust the official
	// registry, and showing the hint about it, while no registry is trusted.
	// Absent means ask; true is the same as absent.
	RegistryPrompt *bool       `yaml:"registry_prompt,omitempty"`
	Presets        []PresetRef `yaml:"presets,omitempty"`
	// Notifications false stops the interface notifying the terminal when a
	// coding agent asks to add a project, open a passthrough or show a
	// document. The prompts still open. Absent means notify.
	Notifications *bool `yaml:"notifications,omitempty"`
	// KeepRunning is what quitting the interface does to the Nearhost it
	// started: true leaves it running in the background, false stops it.
	// Absent means ask.
	KeepRunning *bool `yaml:"keep_running,omitempty"`
	// Network, when present and enabled, has the core serve its page and
	// projects to paired devices on the local network under a domain.
	Network *Network `yaml:"network,omitempty"`
}

// NotificationsOff reports whether the config holds notifications: false.
func (c *Config) NotificationsOff() bool {
	return c.Notifications != nil && !*c.Notifications
}

// RegistryPromptOff reports whether the config holds registry_prompt: false.
func (c *Config) RegistryPromptOff() bool {
	return c.RegistryPrompt != nil && !*c.RegistryPrompt
}

// Trusted finds a trusted registry by alias.
func (c *Config) Trusted(alias string) (Registry, bool) {
	for _, r := range c.Registries {
		if r.Alias == alias {
			return r, true
		}
	}
	return Registry{}, false
}

// Preset finds an enabled preset by name.
func (c *Config) Preset(name string) (PresetRef, bool) {
	for _, p := range c.Presets {
		if p.Name == name {
			return p, true
		}
	}
	return PresetRef{}, false
}

// UpsertPreset replaces the enabled preset with the same name or appends it.
func (c *Config) UpsertPreset(p PresetRef) {
	for i := range c.Presets {
		if c.Presets[i].Name == p.Name {
			c.Presets[i] = p
			return
		}
	}
	c.Presets = append(c.Presets, p)
}

// RemovePreset disables the named preset and reports whether it was enabled.
func (c *Config) RemovePreset(name string) bool {
	for i := range c.Presets {
		if c.Presets[i].Name == name {
			c.Presets = append(c.Presets[:i], c.Presets[i+1:]...)
			return true
		}
	}
	return false
}

// Load reads the config at path. A missing file yields an empty config. An
// empty file is an error: a crash part way through a write can leave one,
// and reading it as no projects would let the next save write over them.
func Load(path string) (*Config, error) {
	c, _, err := load(path)
	return c, err
}

// readFile reads a file for Load and UnknownKeys. Tests replace it to count
// the reads.
var readFile = os.ReadFile

// load is Load that also returns the bytes it read, nil for a missing file,
// so Update can look for unknown keys in the same version of the file it
// changes and saves.
func load(path string) (*Config, []byte, error) {
	data, err := readFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read config: %w", err)
	}
	if len(data) == 0 {
		return nil, nil, fmt.Errorf("%s is empty. Restore it from a backup, or delete it to start afresh.", path)
	}
	c, err := Parse(data)
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return c, data, nil
}

// errEmpty is Parse's error for no data. Load names the file instead.
var errEmpty = errors.New("the config is empty")

// Parse reads a config from the YAML that Marshal writes, as Load does once
// it has read the file. No data is an error, as an empty file is to Load.
// Like Load, it does not validate what it reads.
func Parse(data []byte) (*Config, error) {
	if len(data) == 0 {
		return nil, errEmpty
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Marshal is the YAML that Save writes for c.
func Marshal(c *Config) ([]byte, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("encode config: %w", err)
	}
	return data, nil
}

// Unknown is a key in the config file that Nearhost does not know, with the
// line it is on. Load ignores such a key and Save would not write it back.
type Unknown struct {
	Key  string
	Line int
}

// Describe says where in the file at path the key is and that Nearhost does
// not know it, as one sentence.
func (u Unknown) Describe(path string) string {
	return fmt.Sprintf("%s line %d: Nearhost does not know the key %q.", path, u.Line, u.Key)
}

// UnknownKeysError is the error Update returns when the file holds keys
// Nearhost does not know, which saving would delete.
type UnknownKeysError struct {
	Path string
	Keys []Unknown
}

// Error names every unknown key with its line, one per line.
func (e *UnknownKeysError) Error() string {
	lines := make([]string, len(e.Keys))
	for i, u := range e.Keys {
		lines[i] = u.Describe(e.Path) + " Fix or remove it; saving now would delete it."
	}
	return strings.Join(lines, "\n")
}

// Report is the message for the keys, config.unknown-keys: where each is,
// that saving would delete them, and to fix or remove them. Its data holds
// the file and each key with its line.
func (e *UnknownKeysError) Report() *report.Message {
	keys := make([]map[string]any, len(e.Keys))
	for i, u := range e.Keys {
		keys[i] = map[string]any{"key": u.Key, "line": u.Line}
	}
	var m *report.Message
	if len(e.Keys) == 1 {
		m = report.Errorf(report.ConfigUnknownKeys, "%s", e.Keys[0].Describe(e.Path)).
			Because("Saving now would delete it.").
			Then("Fix or remove it.")
	} else {
		named := make([]string, len(e.Keys))
		for i, u := range e.Keys {
			named[i] = fmt.Sprintf("%q on line %d", u.Key, u.Line)
		}
		list := strings.Join(named[:len(named)-1], ", ") + " and " + named[len(named)-1]
		m = report.Errorf(report.ConfigUnknownKeys, "%s has keys Nearhost does not know: %s.", e.Path, list).
			Because("Saving now would delete them.").
			Then("Fix or remove them.")
	}
	return m.With("path", e.Path).With("keys", keys)
}

// Unwrap is the report message, so report.As finds it.
func (e *UnknownKeysError) Unwrap() error { return e.Report() }

// UnknownKeys reads the config at path again and returns, in file order,
// every key Nearhost does not know: one the structs have no field for, such
// as host: written for hosts:, or one a newer Nearhost wrote. A missing file
// has none.
//
// It walks the YAML node tree against the structs' yaml tags rather than
// decode with yaml's KnownFields, whose errors are text to be parsed back
// apart and mix unknown keys with every other type error. The keys of a map,
// if the config ever has one, are data and never unknown, and a type that
// decodes itself is not looked into. Comments and key order, which Save does
// not keep either, are not reported.
func UnknownKeys(path string) ([]Unknown, error) {
	data, err := readFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		// A PathError names the file already, as "open <path>: ...".
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return unknownKeysIn(path, data)
}

// unknownKeysIn is UnknownKeys for data, the content of the file at path,
// which it names in a parse error.
func unknownKeysIn(path string, data []byte) ([]Unknown, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var out []Unknown
	unknownIn(&doc, reflect.TypeFor[Config](), &out)
	return out, nil
}

var unmarshalerType = reflect.TypeFor[yaml.Unmarshaler]()

// unknownIn appends to out the keys under n that type t has no field for.
// An alias is skipped: its anchor is checked where it is written, against
// the type there, so an anchor written under one type and aliased under
// another is checked only as the first.
func unknownIn(n *yaml.Node, t reflect.Type, out *[]Unknown) {
	if n.Kind == yaml.DocumentNode {
		for _, c := range n.Content {
			unknownIn(c, t, out)
		}
		return
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if n.Kind == yaml.AliasNode || reflect.PointerTo(t).Implements(unmarshalerType) {
		return
	}
	switch {
	case t.Kind() == reflect.Struct && n.Kind == yaml.MappingNode:
		fields, anyKey := yamlFields(t)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.ShortTag() == "!!merge" {
				// A merge key brings in one mapping or a list of
				// them, checked against the same struct.
				merged := []*yaml.Node{v}
				if v.Kind == yaml.SequenceNode {
					merged = v.Content
				}
				for _, m := range merged {
					unknownIn(m, t, out)
				}
				continue
			}
			ft, ok := fields[k.Value]
			switch {
			case ok:
				unknownIn(v, ft, out)
			case anyKey == nil:
				*out = append(*out, Unknown{Key: k.Value, Line: k.Line})
			default:
				unknownIn(v, anyKey, out)
			}
		}
	case (t.Kind() == reflect.Slice || t.Kind() == reflect.Array) && n.Kind == yaml.SequenceNode:
		for _, c := range n.Content {
			unknownIn(c, t.Elem(), out)
		}
	case t.Kind() == reflect.Map && n.Kind == yaml.MappingNode:
		for i := 1; i < len(n.Content); i += 2 {
			unknownIn(n.Content[i], t.Elem(), out)
		}
	}
}

// yamlFields maps each key the struct t decodes to its field's type, as
// yaml.v3 names them: the tag's name, or the field's name in lowercase, with
// the fields of an inline struct in among them. anyKey is the element type of
// an inline map, which takes every other key, or nil when there is none.
func yamlFields(t reflect.Type) (fields map[string]reflect.Type, anyKey reflect.Type) {
	fields = map[string]reflect.Type{}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() && !f.Anonymous {
			continue
		}
		tag := f.Tag.Get("yaml")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if slices.Contains(strings.Split(opts, ","), "inline") {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Map {
				anyKey = ft.Elem()
				continue
			}
			inner, innerAny := yamlFields(ft)
			for k, v := range inner {
				fields[k] = v
			}
			if innerAny != nil {
				anyKey = innerAny
			}
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		fields[name] = f.Type
	}
	return fields, anyKey
}

// Save writes the config atomically with WriteFileAtomic, creating its
// directory 0700 when missing. A config kept as a link stays a link, and the
// file keeps its mode; a new one is 0644.
func Save(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := Marshal(c)
	if err != nil {
		return err
	}
	if err := WriteFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// Project finds a project by name.
func (c *Config) Project(name string) (*Project, bool) {
	for i := range c.Projects {
		if c.Projects[i].Name == name {
			return &c.Projects[i], true
		}
	}
	return nil, false
}

// Server finds a server by name. The local server is always present.
func (c *Config) Server(name string) (Server, bool) {
	if name == LocalServer {
		return Server{Name: LocalServer}, true
	}
	for _, s := range c.Servers {
		if s.Name == name {
			return s, true
		}
	}
	return Server{}, false
}

// UpsertProject replaces the project with the same name or appends it.
func (c *Config) UpsertProject(p Project) {
	for i := range c.Projects {
		if c.Projects[i].Name == p.Name {
			c.Projects[i] = p
			return
		}
	}
	c.Projects = append(c.Projects, p)
}

// RemoveProject deletes the named project and reports whether it existed.
func (c *Config) RemoveProject(name string) bool {
	for i := range c.Projects {
		if c.Projects[i].Name == name {
			c.Projects = append(c.Projects[:i], c.Projects[i+1:]...)
			return true
		}
	}
	return false
}

// validateChange checks what a change must not bring in although a config
// already holding it still loads, so a config that loaded before keeps
// loading and saving:
//   - a main name that ValidProjectAlias refuses, of a project that is new
//     since before or whose main name changed. The engine does not start a
//     project with such a name, and doctor reports one already in the file.
//   - an exact host of an enabled project under a pattern of another, a pair
//     before did not have. Doctor warns about one already in the file.
func (c *Config) validateChange(before *Config) error {
	for _, p := range c.Projects {
		if old, ok := before.Project(p.Name); ok && old.Alias == p.Alias {
			continue
		}
		if err := ValidProjectAlias(p.Alias); err != nil {
			return invalid("projects.alias", fmt.Errorf("project %q: %w", p.Name, err))
		}
	}
	had := patternPairs(before.PatternConflicts())
	for _, hc := range c.PatternConflicts() {
		if !had[hc.pair()] {
			return invalid("projects.routes.hosts", hc)
		}
	}
	return nil
}

// Validate checks the invariants the rest of nearhost relies on.
func (c *Config) Validate() error {
	if c.Network != nil {
		if err := c.Network.validate(); err != nil {
			return err
		}
	}
	serverNames := map[string]bool{LocalServer: true}
	for _, s := range c.Servers {
		if s.Name == LocalServer {
			return invalid("servers.name", fmt.Errorf("server name %q is reserved", LocalServer))
		}
		if s.Name == "" || s.SSHHost == "" {
			field := "servers.ssh_host"
			if s.Name == "" {
				field = "servers.name"
			}
			return invalid(field, fmt.Errorf("server %q needs a name and ssh_host", s.Name))
		}
		if strings.HasPrefix(s.SSHHost, "-") {
			return invalid("servers.ssh_host", fmt.Errorf("server %q: ssh_host must not start with %q, ssh would read it as an option", s.Name, "-"))
		}
		if serverNames[s.Name] {
			return invalid("servers.name", fmt.Errorf("duplicate server name %q", s.Name))
		}
		serverNames[s.Name] = true
	}

	names := map[string]bool{}
	aliases := map[string]string{}
	loopbacks := map[int]string{}
	for _, p := range c.Projects {
		if p.Name == "" {
			return invalid("projects.name", errors.New("project with empty name"))
		}
		if names[p.Name] {
			return invalid("projects.name", fmt.Errorf("duplicate project name %q", p.Name))
		}
		names[p.Name] = true
		if !serverNames[p.Server] {
			return invalid("projects.server", fmt.Errorf("project %q uses unknown server %q", p.Name, p.Server))
		}
		if p.Alias == "" {
			return invalid("projects.alias", fmt.Errorf("project %q has no alias", p.Name))
		}
		aliasKey := strings.ToLower(p.Alias) // hostnames are case-insensitive
		if other, dup := aliases[aliasKey]; dup {
			return invalid("projects.alias", fmt.Errorf("alias %q used by both %q and %q", p.Alias, other, p.Name))
		}
		aliases[aliasKey] = p.Name
		if strings.EqualFold(p.Alias, hostname.PageHost) {
			return invalid("projects.alias", fmt.Errorf("project %q: %w", p.Name, errPageHost))
		}

		if p.DockerClients() && p.Server != LocalServer {
			// Containers can only reach Nearhost on this machine.
			return invalid("projects.docker_clients", fmt.Errorf("project %q: docker_clients works only for projects on this machine", p.Name))
		}
		if p.Nearhostrc != nil {
			for _, r := range p.Nearhostrc.Routes {
				for _, h := range r.Hosts {
					if err := ValidProjectHost(h); err != nil {
						return invalid("projects.nearhostrc.routes.hosts", fmt.Errorf("project %q approved .nearhostrc port %d: %w", p.Name, r.Port, err))
					}
				}
			}
		}

		webCount := 0
		hosts := map[string]bool{strings.ToLower(p.Alias): true}
		for _, r := range p.Routes {
			if !r.Role.Valid() {
				return invalid("projects.routes.role", fmt.Errorf("project %q port %d has unknown role %q", p.Name, r.Port, r.Role))
			}
			if r.Port < 1 || r.Port > 65535 {
				return invalid("projects.routes.port", fmt.Errorf("project %q has invalid port %d", p.Name, r.Port))
			}
			if r.Role != RoleWeb && len(r.Hosts) > 0 {
				return invalid("projects.routes.hosts", fmt.Errorf("project %q port %d: only web routes have hosts", p.Name, r.Port))
			}
			switch r.Role {
			case RoleWeb:
				webCount++
				if webCount > 1 && len(r.Hosts) == 0 {
					return invalid("projects.routes.hosts", fmt.Errorf("project %q: web route on port %d needs at least one host; only the first web route is served at the alias", p.Name, r.Port))
				}
				for _, h := range r.Hosts {
					if err := ValidProjectHost(h); err != nil {
						return invalid("projects.routes.hosts", fmt.Errorf("project %q port %d: %w", p.Name, r.Port, err))
					}
					// Web() drops the alias from the primary route's own
					// hosts, so only there is it harmless.
					if webCount == 1 && strings.EqualFold(h, p.Alias) {
						continue
					}
					if hosts[strings.ToLower(h)] {
						return invalid("projects.routes.hosts", fmt.Errorf("project %q serves host %q twice", p.Name, h))
					}
					hosts[strings.ToLower(h)] = true
				}
			case RoleLoopback:
				if p.Enabled {
					if other, clash := loopbacks[r.Port]; clash {
						return invalid("projects.routes.port", fmt.Errorf("loopback port %d used by both %q and %q", r.Port, other, p.Name))
					}
					loopbacks[r.Port] = p.Name
				}
			}
		}
		if webCount == 0 {
			return invalid("projects.routes", fmt.Errorf("project %q must have at least one web route", p.Name))
		}
	}

	trusted := map[string]bool{}
	repos := map[string]string{}
	for _, r := range c.Registries {
		// The alias becomes a directory name, so it must be a plain word.
		if !ValidAlias(r.Alias) {
			return invalid("registries.alias", fmt.Errorf("registry alias %q must be lowercase letters and digits, with single hyphens between words", r.Alias))
		}
		if !ValidRepo(r.Repo) {
			return invalid("registries.repo", fmt.Errorf("registry %s: repo %q is not a GitHub repository written owner/name", r.Alias, r.Repo))
		}
		if trusted[r.Alias] {
			return invalid("registries.alias", fmt.Errorf("duplicate registry alias %q", r.Alias))
		}
		trusted[r.Alias] = true
		repo := strings.ToLower(r.Repo) // GitHub names are case-insensitive
		if other, dup := repos[repo]; dup {
			return invalid("registries.repo", fmt.Errorf("repo %s is trusted twice, as %q and %q", r.Repo, other, r.Alias))
		}
		repos[repo] = r.Alias
	}

	presets := map[string]bool{}
	for _, p := range c.Presets {
		// The name becomes a file path, so it must be one or two plain words.
		if !ValidPresetName(p.Name) {
			return invalid("presets.name", fmt.Errorf("preset name %q must be lowercase letters, digits and hyphens, or <registry>/<name>", p.Name))
		}
		alias, _ := SplitPreset(p.Name)
		switch {
		case alias != "" && !trusted[alias]:
			return invalid("presets.name", fmt.Errorf("preset %q comes from registry %q, which is not in registries; add the registry back under registries or remove the preset from presets", p.Name, alias))
		case alias != "" && (p.Ref == "" || p.SHA256 == ""):
			return invalid("presets.ref", fmt.Errorf("preset %q comes from a registry, so it needs a ref and a sha256", p.Name))
		case alias == "" && (p.Ref != "" || p.SHA256 != ""):
			return invalid("presets.ref", fmt.Errorf("preset %q is a local file, so it has no ref or sha256; a preset from a registry is named <registry>/<name>", p.Name))
		}
		if presets[p.Name] {
			return invalid("presets.name", fmt.Errorf("duplicate preset %q", p.Name))
		}
		presets[p.Name] = true
	}
	return nil
}

// invalid is a Validate error, config.invalid: err's text, with the key it
// is about (such as "projects.alias") as the field in its data. Its cause is
// err, so errors.Is and errors.As see what err wraps.
func invalid(field string, err error) error {
	return report.Errorf(report.ConfigInvalid, "%s", err).With("field", field).Wrap(err)
}

var (
	presetName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	repoOwner  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	repoName   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// ValidAlias reports whether s can be a registry alias: lowercase letters and
// digits, with single hyphens between words.
func ValidAlias(s string) bool { return presetName.MatchString(s) }

// ValidRepo reports whether s names a GitHub repository as owner/name.
func ValidRepo(s string) bool {
	owner, name, ok := strings.Cut(s, "/")
	return ok && repoOwner.MatchString(owner) && repoName.MatchString(name) && name != "." && name != ".."
}

// ValidPresetName reports whether s can name an enabled preset: a plain name
// for a local file, or <alias>/<name> for a preset from a registry.
func ValidPresetName(s string) bool {
	alias, name, qualified := strings.Cut(s, "/")
	if !qualified {
		return presetName.MatchString(s)
	}
	return presetName.MatchString(alias) && presetName.MatchString(name)
}

// SplitPreset splits an enabled preset's name into its registry's alias and
// its own name. The alias of a local preset is empty.
func SplitPreset(name string) (alias, plain string) {
	if alias, plain, ok := strings.Cut(name, "/"); ok {
		return alias, plain
	}
	return "", name
}

var nonAlias = regexp.MustCompile(`[^a-z0-9]+`)

// maxLabel is the longest DNS label.
const maxLabel = 63

// LosesInAlias reports whether AliasFor would drop part of name: a letter,
// digit, mark or symbol outside ASCII, such as é, ™ or a combining accent.
// Non-ASCII spaces and punctuation, such as a non-breaking space or an en
// dash, become a hyphen as their ASCII forms do, so they lose nothing.
func LosesInAlias(name string) bool {
	for _, r := range name {
		if r > unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || unicode.IsSymbol(r)) {
			return true
		}
	}
	return false
}

// AliasFor derives a .test hostname from a project name. A slug longer than
// one DNS label is cut short and given a hash suffix so distinct names stay
// distinct.
func AliasFor(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = nonAlias.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "project"
	}
	if len(s) > maxLabel {
		sum := fmt.Sprintf("%x", sha256.Sum256([]byte(s)))[:8]
		s = strings.TrimRight(s[:maxLabel-len(sum)-1], "-") + "-" + sum
	}
	return s + ".test"
}

// MovePrimary returns a copy of routes in which the route on port sits just
// before the first other web route, so that it is the primary one. The order
// is unchanged when that route is already the first web route, is not a web
// route, or is absent. routes itself is not modified.
func MovePrimary(routes []Route, port int) []Route {
	out := append([]Route(nil), routes...)
	at, first := -1, -1
	for i, r := range out {
		if r.Role != RoleWeb {
			continue
		}
		if first < 0 {
			first = i
		}
		if r.Port == port && at < 0 {
			at = i
		}
	}
	if at <= first {
		return out
	}
	r := out[at]
	copy(out[first+1:at+1], out[first:at])
	out[first] = r
	return out
}

// SetRole returns a copy of routes with the route on port given role. A
// route made web takes the place of every web route without hosts of its
// own, which are ignored, and when it has no hosts itself it becomes the
// primary, the only web route that needs none. A route made anything else
// loses its hosts, which only a web route has. routes itself is not
// modified, and nothing changes when no route is on port.
func SetRole(routes []Route, port int, role Role) []Route {
	out := append([]Route(nil), routes...)
	at := slices.IndexFunc(out, func(r Route) bool { return r.Port == port })
	if at < 0 {
		return out
	}
	for i := range out {
		if i != at && role == RoleWeb && out[i].Role == RoleWeb && len(out[i].Hosts) == 0 {
			out[i].Role = RoleIgnore
		}
	}
	out[at].Role = role
	if role != RoleWeb {
		out[at].Hosts = nil
		return out
	}
	if len(out[at].Hosts) > 0 {
		return out
	}
	return MovePrimary(out, port)
}

// WithoutAliasHosts is a copy of routes without the alias among the hosts
// of web routes other than the primary. The primary is served at the alias
// already, so a preset that names it for another route, as REVERB_HOST does
// when it is the app's own name, would serve it twice. A route left without
// hosts has nothing of its own to serve, so it is ignored.
func WithoutAliasHosts(routes []Route, alias string) []Route {
	out := make([]Route, len(routes))
	primary := true
	for i, r := range routes {
		out[i] = r
		if r.Role != RoleWeb {
			continue
		}
		if primary {
			primary = false
			continue
		}
		var kept []string
		for _, h := range r.Hosts {
			if !strings.EqualFold(h, alias) {
				kept = append(kept, h)
			}
		}
		if len(kept) == len(r.Hosts) {
			continue
		}
		out[i].Hosts = kept
		if len(kept) == 0 {
			out[i].Role = RoleIgnore
		}
	}
	return out
}

// UpCommand is the command line that starts the project called name, fit to
// be copied into a shell: a name that is not one plain word is quoted, and one
// that begins with a dash is kept from being read as a flag.
func UpCommand(name string) string {
	plain := name != ""
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-+:@%,=", c)) {
			plain = false
			break
		}
	}
	if !plain {
		name = "'" + strings.ReplaceAll(name, "'", `'\''`) + "'"
	}
	if strings.HasPrefix(name, "-") {
		return "nearhost up -- " + name
	}
	return "nearhost up " + name
}
