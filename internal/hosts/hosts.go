// Package hosts manages the nearhost block in /etc/hosts.
package hosts

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"sort"
	"strings"
)

const (
	StartMarker = "# nearhost:start"
	EndMarker   = "# nearhost:end"
	DefaultPath = "/etc/hosts"
)

// block renders the managed lines for aliases, or nil when there are none.
func block(aliases []string) []string {
	if len(aliases) == 0 {
		return nil
	}
	uniq := map[string]bool{}
	var sorted []string
	for _, a := range aliases {
		a = strings.TrimSpace(a)
		if a == "" || uniq[a] {
			continue
		}
		uniq[a] = true
		sorted = append(sorted, a)
	}
	sort.Strings(sorted)
	lines := []string{StartMarker}
	for _, a := range sorted {
		lines = append(lines, "127.0.0.1 "+a)
	}
	return append(lines, EndMarker)
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func findBlock(lines []string) (start, end int) {
	start, end = -1, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if start < 0 && t == StartMarker {
			start = i
			continue
		}
		if start >= 0 && t == EndMarker {
			return start, i
		}
	}
	return start, -1
}

func isEntry(line string) bool {
	f := strings.Fields(line)
	return len(f) == 2 && f[0] == "127.0.0.1"
}

// unchanged reports whether updated differs from existing at most by a
// missing trailing newline.
func unchanged(existing, updated string) bool {
	return updated == existing || updated == existing+"\n"
}

// Rewrite returns the hosts content with the managed block set to aliases.
// Everything outside the markers is preserved byte for byte.
func Rewrite(existing string, aliases []string) string {
	lines := splitLines(existing)
	start, end := findBlock(lines)
	var before, after []string
	switch {
	case start >= 0 && end >= 0:
		before, after = lines[:start], lines[end+1:]
	case start >= 0:
		// Unterminated block: drop only the orphan marker and the block
		// entries right after it; everything else is foreign content.
		n := start + 1
		for n < len(lines) && isEntry(lines[n]) {
			n++
		}
		before = append(append([]string{}, lines[:start]...), lines[n:]...)
	default:
		before = lines
	}
	out := make([]string, 0, len(before)+len(aliases)+2+len(after))
	out = append(out, before...)
	out = append(out, block(aliases)...)
	out = append(out, after...)
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n") + "\n"
}

// Aliases returns the aliases currently in the managed block.
func Aliases(existing string) []string {
	lines := splitLines(existing)
	start, end := findBlock(lines)
	if start < 0 || end < 0 {
		return nil
	}
	var out []string
	for _, l := range lines[start+1 : end] {
		if isEntry(l) {
			out = append(out, strings.Fields(l)[1])
		}
	}
	return out
}

func read(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(data), nil
}

// Apply rewrites the file in place. It needs write permission on path.
func Apply(path string, aliases []string) error {
	existing, err := read(path)
	if err != nil {
		return err
	}
	updated := Rewrite(existing, aliases)
	if unchanged(existing, updated) {
		return nil
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Applier performs the privileged write, typically via sudo.
type Applier func(aliases []string) error

// Ensure compares the file with the desired block and calls apply only when
// a change is needed.
func Ensure(path string, aliases []string, apply Applier) (bool, error) {
	existing, err := read(path)
	if err != nil {
		return false, err
	}
	if unchanged(existing, Rewrite(existing, aliases)) {
		return false, nil
	}
	if err := apply(aliases); err != nil {
		return false, err
	}
	return true, nil
}

// SudoApplier runs `sudo <executable> hosts apply --file <path>` with aliases on stdin.
func SudoApplier(executable, path string) Applier {
	return func(aliases []string) error {
		cmd := sudoCommand(executable, path, aliases)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("sudo hosts apply: %w", err)
		}
		return nil
	}
}

func sudoCommand(executable, path string, aliases []string) *exec.Cmd {
	cmd := exec.Command("sudo", "-p", "nearhost needs your password to update /etc/hosts: ", executable, "hosts", "apply", "--file", path)
	cmd.Stdin = strings.NewReader(strings.Join(aliases, "\n") + "\n")
	return cmd
}
