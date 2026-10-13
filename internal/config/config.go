// Package config models and persists the nearhost configuration file.
package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Role says what nearhost does with a discovered port.
type Role string

const (
	// RoleWeb is served at https://<alias>. Exactly one per project.
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

// WebRoute returns the project's single web route, if present.
func (p Project) WebRoute() (Route, bool) {
	for _, r := range p.Routes {
		if r.Role == RoleWeb {
			return r, true
		}
	}
	return Route{}, false
}

// Server is a machine reachable over SSH.
type Server struct {
	Name    string `yaml:"name"`
	SSHHost string `yaml:"ssh_host"`
}

// Config is the whole configuration file.
type Config struct {
	Servers  []Server  `yaml:"servers"`
	Projects []Project `yaml:"projects"`
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
		for _, r := range p.Routes {
			if !r.Role.Valid() {
				return fmt.Errorf("project %q port %d has unknown role %q", p.Name, r.Port, r.Role)
			}
			if r.Port < 1 || r.Port > 65535 {
				return fmt.Errorf("project %q has invalid port %d", p.Name, r.Port)
			}
			switch r.Role {
			case RoleWeb:
				webCount++
			case RoleLoopback:
				if p.Enabled {
					if other, clash := loopbacks[r.Port]; clash {
						return fmt.Errorf("loopback port %d used by both %q and %q", r.Port, other, p.Name)
					}
					loopbacks[r.Port] = p.Name
				}
			}
		}
		if webCount != 1 {
			return fmt.Errorf("project %q must have exactly one web route, has %d", p.Name, webCount)
		}
	}
	return nil
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
