// Package transport runs OpenSSH to reach remote servers and forward ports.
package transport

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kevinburke/ssh_config"
)

// maxIncludeDepth matches OpenSSH's limit on nested Include directives.
const maxIncludeDepth = 16

// ListHosts returns concrete Host aliases from an ssh_config, in file order,
// without wildcard or negated patterns and without duplicates. Include
// directives are followed; relative paths resolve against ~/.ssh, and missing
// targets are ignored.
func ListHosts(r io.Reader) ([]string, error) {
	l := &hostLister{seen: map[string]bool{}}
	if home, err := os.UserHomeDir(); err == nil {
		l.sshDir = filepath.Join(home, ".ssh")
		l.home = home
	}
	if err := l.read(r, nil); err != nil {
		return nil, err
	}
	return l.hosts, nil
}

type hostLister struct {
	home, sshDir string
	seen         map[string]bool
	hosts        []string
}

// read scans r, handing runs of non-Include lines to the ssh_config parser and
// expanding Include lines in place so host order follows the file. chain holds
// the files currently being read, to stop include cycles.
func (l *hostLister) read(r io.Reader, chain []string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	var chunk []string
	flush := func() error {
		if len(chunk) == 0 {
			return nil
		}
		text := strings.Join(chunk, "\n")
		chunk = chunk[:0]
		return l.add(text)
	}
	for _, line := range strings.Split(string(data), "\n") {
		args, ok := includeArgs(line)
		if !ok {
			chunk = append(chunk, line)
			continue
		}
		if err := flush(); err != nil {
			return err
		}
		if err := l.include(args, chain); err != nil {
			return err
		}
	}
	return flush()
}

func (l *hostLister) add(text string) error {
	cfg, err := ssh_config.Decode(strings.NewReader(text))
	if err != nil {
		return fmt.Errorf("parse ssh config: %w", err)
	}
	for _, h := range cfg.Hosts {
		for _, p := range h.Patterns {
			name := p.String()
			if name == "" || strings.ContainsAny(name, "*?!") || l.seen[name] {
				continue
			}
			l.seen[name] = true
			l.hosts = append(l.hosts, name)
		}
	}
	return nil
}

func (l *hostLister) include(args, chain []string) error {
	if len(chain) >= maxIncludeDepth {
		return nil
	}
	for _, arg := range args {
		path := arg
		switch {
		case strings.HasPrefix(path, "~/") && l.home != "":
			path = filepath.Join(l.home, path[2:])
		case !filepath.IsAbs(path) && l.sshDir != "":
			path = filepath.Join(l.sshDir, path)
		}
		matches, err := filepath.Glob(path)
		if err != nil {
			return fmt.Errorf("ssh config include %q: %w", arg, err)
		}
		for _, m := range matches {
			if slices.Contains(chain, m) {
				continue
			}
			f, err := os.Open(m)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return err
			}
			err = l.read(f, append(chain[:len(chain):len(chain)], m))
			f.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// includeArgs reports whether line is an Include directive and returns its
// arguments.
func includeArgs(line string) ([]string, bool) {
	line = strings.TrimSpace(line)
	if len(line) < 8 || !strings.EqualFold(line[:7], "include") {
		return nil, false
	}
	if c := line[7]; c != ' ' && c != '\t' && c != '=' {
		return nil, false
	}
	rest := strings.TrimLeft(line[7:], " \t=")
	var args []string
	var tok strings.Builder
	inQuote, have := false, false
	end := func() {
		if have {
			args = append(args, tok.String())
		}
		tok.Reset()
		have = false
	}
loop:
	for _, c := range rest {
		switch {
		case c == '"':
			inQuote = !inQuote
			have = true
		case inQuote:
			tok.WriteRune(c)
		case c == '#':
			break loop
		case c == ' ' || c == '\t':
			end()
		default:
			tok.WriteRune(c)
			have = true
		}
	}
	end()
	return args, true
}

// DefaultHosts lists hosts from ~/.ssh/config. A missing file is not an error.
func DefaultHosts() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, ".ssh", "config")
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	l := &hostLister{seen: map[string]bool{}, home: home, sshDir: filepath.Join(home, ".ssh")}
	if err := l.read(f, []string{path}); err != nil {
		return nil, err
	}
	return l.hosts, nil
}
