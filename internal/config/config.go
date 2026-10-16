// Package config models and persists the nearhost configuration file.
package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nearhost/nearhost/internal/hostname"
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
// serves. Two exact hosts clash when equal and two patterns when written the
// same, ignoring case. An exact host under another project's pattern does not
// clash, because the exact host is the more specific match and wins. The
// project in c named like p is skipped, so a stored project can be checked
// against its own config.
func (c *Config) HostClash(p Project) (host, other string, clash bool) {
	taken := map[string]string{}
	for _, q := range c.Projects {
		if q.Name == p.Name || !q.Enabled {
			continue
		}
		for _, h := range q.AllHosts() {
			if _, seen := taken[strings.ToLower(h)]; !seen {
				taken[strings.ToLower(h)] = q.Name
			}
		}
	}
	for _, h := range p.AllHosts() {
		if name, ok := taken[strings.ToLower(h)]; ok {
			return h, name, true
		}
	}
	return "", "", false
}

// ExactHosts lists the exact hosts of every project, sorted and without
// duplicates, leaving out localhost and wildcard patterns. These are the
// candidates for the hosts file, which resolves neither of those.
func (c *Config) ExactHosts() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range c.Projects {
		for _, h := range p.AllHosts() {
			h = strings.ToLower(h)
			if h == "localhost" || hostname.IsPattern(h) || seen[h] {
				continue
			}
			seen[h] = true
			out = append(out, h)
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
	// coding agent proposes a project. Absent means notify.
	Notifications *bool `yaml:"notifications,omitempty"`
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

// Load reads the config at path. A missing file yields an empty config.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &c, nil
}

// Save writes the config atomically, creating parent directories.
func Save(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("replace config: %w", err)
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

// Validate checks the invariants the rest of nearhost relies on.
func (c *Config) Validate() error {
	serverNames := map[string]bool{LocalServer: true}
	for _, s := range c.Servers {
		if s.Name == LocalServer {
			return fmt.Errorf("server name %q is reserved", LocalServer)
		}
		if s.Name == "" || s.SSHHost == "" {
			return fmt.Errorf("server %q needs a name and ssh_host", s.Name)
		}
		if serverNames[s.Name] {
			return fmt.Errorf("duplicate server name %q", s.Name)
		}
		serverNames[s.Name] = true
	}

	names := map[string]bool{}
	aliases := map[string]string{}
	loopbacks := map[int]string{}
	for _, p := range c.Projects {
		if p.Name == "" {
			return errors.New("project with empty name")
		}
		if names[p.Name] {
			return fmt.Errorf("duplicate project name %q", p.Name)
		}
		names[p.Name] = true
		if !serverNames[p.Server] {
			return fmt.Errorf("project %q uses unknown server %q", p.Name, p.Server)
		}
		if p.Alias == "" {
			return fmt.Errorf("project %q has no alias", p.Name)
		}
		aliasKey := strings.ToLower(p.Alias) // hostnames are case-insensitive
		if other, dup := aliases[aliasKey]; dup {
			return fmt.Errorf("alias %q used by both %q and %q", p.Alias, other, p.Name)
		}
		aliases[aliasKey] = p.Name

		webCount := 0
		hosts := map[string]bool{strings.ToLower(p.Alias): true}
		for _, r := range p.Routes {
			if !r.Role.Valid() {
				return fmt.Errorf("project %q port %d has unknown role %q", p.Name, r.Port, r.Role)
			}
			if r.Port < 1 || r.Port > 65535 {
				return fmt.Errorf("project %q has invalid port %d", p.Name, r.Port)
			}
			if r.Role != RoleWeb && len(r.Hosts) > 0 {
				return fmt.Errorf("project %q port %d: only web routes have hosts", p.Name, r.Port)
			}
			switch r.Role {
			case RoleWeb:
				webCount++
				if webCount > 1 && len(r.Hosts) == 0 {
					return fmt.Errorf("project %q: web route on port %d needs at least one host; only the first web route is served at the alias", p.Name, r.Port)
				}
				for _, h := range r.Hosts {
					if err := hostname.Valid(h); err != nil {
						return fmt.Errorf("project %q port %d: %w", p.Name, r.Port, err)
					}
					// Web() drops the alias from the primary route's own
					// hosts, so only there is it harmless.
					if webCount == 1 && strings.EqualFold(h, p.Alias) {
						continue
					}
					if hosts[strings.ToLower(h)] {
						return fmt.Errorf("project %q serves host %q twice", p.Name, h)
					}
					hosts[strings.ToLower(h)] = true
				}
			case RoleLoopback:
				if p.Enabled {
					if other, clash := loopbacks[r.Port]; clash {
						return fmt.Errorf("loopback port %d used by both %q and %q", r.Port, other, p.Name)
					}
					loopbacks[r.Port] = p.Name
				}
			}
		}
		if webCount == 0 {
			return fmt.Errorf("project %q must have at least one web route", p.Name)
		}
	}

	trusted := map[string]bool{}
	repos := map[string]string{}
	for _, r := range c.Registries {
		// The alias becomes a directory name, so it must be a plain word.
		if !ValidAlias(r.Alias) {
			return fmt.Errorf("registry alias %q must be lowercase letters and digits, with single hyphens between words", r.Alias)
		}
		if !ValidRepo(r.Repo) {
			return fmt.Errorf("registry %s: repo %q is not a GitHub repository written owner/name", r.Alias, r.Repo)
		}
		if trusted[r.Alias] {
			return fmt.Errorf("duplicate registry alias %q", r.Alias)
		}
		trusted[r.Alias] = true
		repo := strings.ToLower(r.Repo) // GitHub names are case-insensitive
		if other, dup := repos[repo]; dup {
			return fmt.Errorf("repo %s is trusted twice, as %q and %q", r.Repo, other, r.Alias)
		}
		repos[repo] = r.Alias
	}

	presets := map[string]bool{}
	for _, p := range c.Presets {
		// The name becomes a file path, so it must be one or two plain words.
		if !ValidPresetName(p.Name) {
			return fmt.Errorf("preset name %q must be lowercase letters, digits and hyphens, or <registry>/<name>", p.Name)
		}
		alias, _ := SplitPreset(p.Name)
		switch {
		case alias != "" && !trusted[alias]:
			return fmt.Errorf("preset %q comes from registry %q, which is not in registries; add the registry back under registries or remove the preset from presets", p.Name, alias)
		case alias != "" && (p.Ref == "" || p.SHA256 == ""):
			return fmt.Errorf("preset %q comes from a registry, so it needs a ref and a sha256", p.Name)
		case alias == "" && (p.Ref != "" || p.SHA256 != ""):
			return fmt.Errorf("preset %q is a local file, so it has no ref or sha256; a preset from a registry is named <registry>/<name>", p.Name)
		}
		if presets[p.Name] {
			return fmt.Errorf("duplicate preset %q", p.Name)
		}
		presets[p.Name] = true
	}
	return nil
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
