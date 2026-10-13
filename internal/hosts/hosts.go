// Package hosts manages the nearhost block in /etc/hosts.
package hosts

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	StartMarker = "# nearhost:start"
	EndMarker   = "# nearhost:end"
	DefaultPath = "/etc/hosts"
)

// validAlias reports whether a is a lowercase DNS hostname ending in .test.
func validAlias(a string) bool {
	const suffix = ".test"
	if len(a) > 253 || !strings.HasSuffix(a, suffix) {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(a, suffix), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// normalize trims, drops blanks, validates, dedupes and sorts aliases.
func normalize(aliases []string) ([]string, error) {
	uniq := map[string]bool{}
	var sorted []string
	for _, a := range aliases {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if !validAlias(a) {
			return nil, fmt.Errorf("invalid alias %q: must be a lowercase hostname ending in .test", a)
		}
		if !uniq[a] {
			uniq[a] = true
			sorted = append(sorted, a)
		}
	}
	sort.Strings(sorted)
	return sorted, nil
}

// block renders the managed lines for already normalized aliases, or nil.
func block(sorted []string) []string {
	if len(sorted) == 0 {
		return nil
	}
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

// isEntry reports whether line is a row nearhost would have written.
func isEntry(line string) bool {
	f := strings.Fields(line)
	return len(f) == 2 && f[0] == "127.0.0.1" && validAlias(f[1])
}

// unchanged reports whether updated differs from existing at most by a
// missing trailing newline.
func unchanged(existing, updated string) bool {
	return updated == existing || updated == existing+"\n"
}

// Rewrite returns the hosts content with the managed block set to aliases.
// Everything outside the markers is preserved byte for byte.
func Rewrite(existing string, aliases []string) (string, error) {
	sorted, err := normalize(aliases)
	if err != nil {
		return "", err
	}
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
	out := make([]string, 0, len(before)+len(sorted)+2+len(after))
	out = append(out, before...)
	out = append(out, block(sorted)...)
	out = append(out, after...)
	if len(out) == 0 {
		return "", nil
	}
	return strings.Join(out, "\n") + "\n", nil
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

// renameFile replaces the target with the finished temporary file. Tests
// replace it to fail or hold the final step.
var renameFile = os.Rename

// Apply rewrites the managed block in the hosts file at path. The file is
// replaced atomically: the new content is written to a temporary file in the
// same directory and renamed over the target, so the path always holds either
// the complete old content or the complete new content. A symlink at path (or
// in its parents) is resolved first and the file it points to is replaced.
// A rename-replace does not carry over extended attributes or ACLs.
func Apply(path string, aliases []string) error {
	existing, err := read(path)
	if err != nil {
		return err
	}
	updated, err := Rewrite(existing, aliases)
	if err != nil {
		return err
	}
	if unchanged(existing, updated) {
		return nil
	}
	return replaceFile(path, updated)
}

// replaceFile atomically replaces the file path resolves to with content.
func replaceFile(path, content string) (err error) {
	target := path
	mode := os.FileMode(0o644)
	var info fs.FileInfo
	if resolved, rerr := filepath.EvalSymlinks(path); rerr == nil {
		target = resolved
		if info, err = os.Stat(target); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(rerr, fs.ErrNotExist) {
		return fmt.Errorf("write %s: %w", path, rerr)
	}

	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".nearhost-*")
	if err != nil {
		return fmt.Errorf("write %s: cannot create a temporary file in directory %s: %w", path, dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	if _, err = tmp.WriteString(content); err != nil {
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err = tmp.Chmod(mode); err != nil {
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if info != nil && os.Geteuid() == 0 {
		if err = chownLike(tmp, info); err != nil {
			return fmt.Errorf("write %s: %w", tmpName, err)
		}
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err = renameFile(tmpName, target); err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	// Best effort: make the rename itself durable.
	if d, derr := os.Open(dir); derr == nil {
		d.Sync()
		d.Close()
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
	updated, err := Rewrite(existing, aliases)
	if err != nil {
		return false, err
	}
	if unchanged(existing, updated) {
		return false, nil
	}
	if err := apply(aliases); err != nil {
		return false, err
	}
	return true, nil
}

// sudoPath is the sudo binary. Tests replace it.
var sudoPath = "sudo"

// sudoStopDelay is how long sudo gets to leave after it was asked to
// terminate before it is killed.
const sudoStopDelay = 2 * time.Second

// SudoApplier runs `sudo <executable> hosts apply --file <path>` with aliases on stdin.
// Nothing interrupts it while sudo waits for a password; a caller that must
// be able to give up uses SudoApplierContext.
func SudoApplier(executable, path string) Applier {
	return SudoApplierContext(context.Background(), executable, path)
}

// SudoApplierContext is SudoApplier bound to ctx. When ctx ends while sudo
// runs, which includes sudo waiting for a password, sudo is asked to
// terminate (and killed if it stays), and the applier returns ctx's error.
// When ctx has already ended sudo is not started.
func SudoApplierContext(ctx context.Context, executable, path string) Applier {
	return func(aliases []string) error {
		cmd := sudoCommand(ctx, executable, path, aliases)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				err = ctxErr
			}
			return fmt.Errorf("sudo hosts apply: %w", err)
		}
		return nil
	}
}

func sudoCommand(ctx context.Context, executable, path string, aliases []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, sudoPath, "-p", "nearhost needs your password to update /etc/hosts: ", executable, "hosts", "apply", "--file", path)
	cmd.Stdin = strings.NewReader(strings.Join(aliases, "\n") + "\n")
	// Terminate rather than kill: at its password prompt sudo then puts the
	// terminal back the way it was.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = sudoStopDelay
	return cmd
}
