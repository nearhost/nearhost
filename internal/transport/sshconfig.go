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

// Limits on what listing hosts reads, since an Include can name any file the
// user's config reaches: at most maxIncludeFiles included files, and the
// first maxConfigBytes of each file.
const (
	maxIncludeFiles = 64
	maxConfigBytes  = 1 << 20
)

// ListHosts returns concrete Host aliases from an ssh_config, in file order,
// without wildcard or negated patterns and without duplicates. Include
// directives are followed; relative paths resolve against ~/.ssh, and missing
// targets are ignored. Match blocks are conditions, not aliases, and are
// skipped. A Host block the parser rejects is skipped too, and the hosts of
// the other blocks are still listed.
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
	// files counts the included files opened so far.
	files int
}

// read scans r, handing runs of non-Include lines to the ssh_config parser and
// expanding Include lines in place so host order follows the file. chain holds
// the files currently being read, to stop include cycles.
func (l *hostLister) read(r io.Reader, chain []string) error {
	data, err := io.ReadAll(io.LimitReader(r, maxConfigBytes))
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
		l.add(text)
		return nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		if isMatchLine(line) {
			// The parser rejects most Match criteria. Dropping the line folds
			// its options into the block above, which changes no Host name.
			continue
		}
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

// add lists the Host names in chunk, a run of lines between Include lines.
// When the parser rejects the chunk, add retries it one block at a time (a
// block starts at a Host line) and skips the blocks that still fail, so one
// bad line cannot empty the list.
func (l *hostLister) add(chunk string) {
	if l.addParsed(chunk) == nil {
		return
	}
	var block []string
	flush := func() {
		if len(block) > 0 {
			_ = l.addParsed(strings.Join(block, "\n"))
			block = block[:0]
		}
	}
	for _, line := range strings.Split(chunk, "\n") {
		if f := strings.Fields(line); len(f) > 0 && strings.EqualFold(strings.SplitN(f[0], "=", 2)[0], "host") {
			flush()
		}
		block = append(block, line)
	}
	flush()
}

// addParsed parses text and lists its Host names; nothing is listed when the
// parse fails.
func (l *hostLister) addParsed(text string) error {
	cfg, err := ssh_config.Decode(strings.NewReader(text))
	if err != nil {
		return fmt.Errorf("parse ssh config: %w", err)
	}
	for _, h := range cfg.Hosts {
		for _, p := range h.Patterns {
			// The parser splits on spaces only; ssh also splits on tabs.
			for _, name := range strings.Fields(p.String()) {
				name = strings.Trim(name, `"`)
				if name == "" || strings.HasPrefix(name, "!") || strings.ContainsAny(name, "*?") || l.seen[name] {
					continue
				}
				l.seen[name] = true
				l.hosts = append(l.hosts, name)
			}
		}
	}
	return nil
}

func (l *hostLister) include(args, chain []string) error {
	if len(chain) >= maxIncludeDepth {
		return nil
	}
	for _, arg := range args {
		if l.files >= maxIncludeFiles {
			return nil
		}
		// OpenSSH expands %-tokens and ${VAR} in Include paths, and ~user to
		// another account's home. None of that is expanded here: skip the path.
		if strings.Contains(arg, "%") || strings.Contains(arg, "${") || (strings.HasPrefix(arg, "~") && !strings.HasPrefix(arg, "~/") && !strings.HasPrefix(arg, `~\`)) {
			continue
		}
		path := arg
		switch {
		case strings.HasPrefix(path, `~\`):
			if l.home == "" {
				return fmt.Errorf("ssh config include %q: home directory is unknown", arg)
			}
			path = filepath.Join(l.home, path[2:])
		case strings.HasPrefix(path, "~/"):
			if l.home == "" {
				return fmt.Errorf("ssh config include %q: home directory is unknown", arg)
			}
			path = filepath.Join(l.home, path[2:])
		case !filepath.IsAbs(path):
			if l.sshDir == "" {
				return fmt.Errorf("ssh config include %q: home directory is unknown", arg)
			}
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
			if fi, err := os.Stat(m); err != nil || !fi.Mode().IsRegular() {
				continue
			}
			if l.files >= maxIncludeFiles {
				return nil
			}
			l.files++
			f, err := os.Open(m)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
					continue
				}
				return err
			}
			// A file the parser rejects is skipped, as OpenSSH's own tools
			// would report it rather than hide the rest of the config.
			_ = l.read(f, append(chain[:len(chain):len(chain)], m))
			f.Close()
		}
	}
	return nil
}

// isMatchLine reports whether line opens a Match block.
func isMatchLine(line string) bool {
	line = strings.TrimSpace(line)
	if len(line) < 6 || !strings.EqualFold(line[:5], "match") {
		return false
	}
	c := line[5]
	return c == ' ' || c == '\t' || c == '='
}

// includeArgs reports whether line is an Include directive and returns its
// arguments.
func includeArgs(line string) ([]string, bool) {
	line = strings.TrimSpace(line)
	if strings.EqualFold(line, "include") {
		// A bare Include names nothing; claim it so the parser never sees it.
		return nil, true
	}
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

// DefaultHosts lists hosts from the user's ssh config (~/.ssh/config, or
// %USERPROFILE%\.ssh\config on Windows) and the files it includes. A missing
// file is not an error. It reads files only: nothing in the config, such as a
// Match exec command, is ever run.
func DefaultHosts() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, ".ssh", "config")
	// Opening a FIFO would block, so look before opening.
	if fi, err := os.Stat(path); err == nil && !fi.Mode().IsRegular() {
		return nil, nil
	}
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
