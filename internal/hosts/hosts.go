// Package hosts manages the nearhost block in the system hosts file.
package hosts

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/nearhost/nearhost/internal/elevate"
	"github.com/nearhost/nearhost/internal/hostname"
)

const (
	StartMarker = "# nearhost:start"
	EndMarker   = "# nearhost:end"
)

// ErrInvalidHost is the error, wrapped with the host, for an alias Nearhost
// does not write to the hosts file.
var ErrInvalidHost = errors.New("invalid host")

// ErrReadOnly is wrapped by the error replaceFile returns when the hosts file
// has the read-only attribute set, which only Windows has.
var ErrReadOnly = errors.New("hosts file is read-only")

// readOnlyError is the error for a hosts file that is read-only. Its message
// says how to clear the attribute; Nearhost does not clear it, because a
// read-only hosts file is often deliberate.
type readOnlyError struct{ path string }

// readOnlyMarker is the fixed part of the read-only message.
const readOnlyMarker = " is read-only. Clear it with: attrib -r "

// Error returns the message shown to the user.
func (e *readOnlyError) Error() string {
	return fmt.Sprintf("%s%s\"%s\" (in an administrator terminal), or run with --no-hosts.", e.path, readOnlyMarker, e.path)
}

// IsReadOnlyMessage reports whether s is the read-only message, for code that
// only has an error's text, such as a project's status reason.
func IsReadOnlyMessage(s string) bool { return strings.Contains(s, readOnlyMarker) }

// Is reports a match with ErrReadOnly.
func (e *readOnlyError) Is(target error) bool { return target == ErrReadOnly }

// isReadOnly reports whether the file at path has the read-only attribute.
// Tests replace it to reach the message on systems without the attribute.
var isReadOnly = readOnly

// validEntry reports whether a is a name nearhost writes to the hosts file:
// a valid exact host, so neither a wildcard pattern nor localhost, which the
// system already resolves.
func validEntry(a string) bool {
	return hostname.Valid(a) == nil && !hostname.IsPattern(a) && a != "localhost"
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
		if !validEntry(a) {
			return nil, fmt.Errorf("%w %q: must be a lowercase host name, not localhost or a wildcard", ErrInvalidHost, a)
		}
		if !uniq[a] {
			uniq[a] = true
			sorted = append(sorted, a)
		}
	}
	sort.Strings(sorted)
	return sorted, nil
}

// blockFor renders the managed lines for already normalized aliases, or nil.
// With ipv6 each name gets an IPv4 and an IPv6 loopback row, so a real domain
// with a public AAAA record cannot send the browser to the live site over
// IPv6; without it the block is the form 0.7.x wrote.
func blockFor(sorted []string, ipv6 bool) []string {
	if len(sorted) == 0 {
		return nil
	}
	lines := []string{StartMarker}
	for _, a := range sorted {
		lines = append(lines, "127.0.0.1 "+a)
		if ipv6 {
			lines = append(lines, "::1 "+a)
		}
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

// isEntry reports whether line is a row nearhost would have written: a
// loopback address of either family and one name. Blocks from 0.7.x hold only
// the IPv4 rows and are still read.
func isEntry(line string) bool {
	f := strings.Fields(line)
	return len(f) == 2 && (f[0] == "127.0.0.1" || f[0] == "::1") && validEntry(f[1])
}

// unchanged reports whether updated differs from existing at most by a
// missing trailing newline.
func unchanged(existing, updated string) bool {
	return updated == existing || updated == existing+"\n" || updated == existing+"\r\n"
}

// defaultLineEnding is the line ending for a hosts file with no line breaks:
// CRLF on Windows, LF elsewhere. Tests set it to reach both cases.
var defaultLineEnding = func() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}()

// lineEnding returns the dominant line ending of the lines outside the
// managed block: CRLF when more than half of their line breaks are CRLF, LF
// when not, and the platform default when there are none. The block itself is
// left out, so an LF block from 0.13.0 in a CRLF file does not sway the vote.
// A lone \r is not a line break, so the caller leaves out a last line that has
// no line break after it.
func lineEnding(parts ...[]string) string {
	total, crlf := 0, 0
	for _, p := range parts {
		for _, l := range p {
			total++
			if strings.HasSuffix(l, "\r") {
				crlf++
			}
		}
	}
	if total <= 0 {
		return defaultLineEnding
	}
	if crlf*2 > total {
		return "\r\n"
	}
	return "\n"
}

// Current reports whether existing already holds the managed block for
// aliases, the check Apply and Ensure make before they write. A missing final
// newline does not count as a difference.
func Current(existing string, aliases []string) (bool, error) {
	updated, err := Rewrite(existing, aliases)
	if err != nil {
		return false, err
	}
	return unchanged(existing, updated), nil
}

// Rewrite returns the hosts content with the managed block set to aliases.
// Everything outside the markers is preserved byte for byte. The block takes
// the file's dominant line ending. When Nearhost had to add a newline after a
// last line that had none, removing the block later leaves that newline.
func Rewrite(existing string, aliases []string) (string, error) {
	return rewrite(existing, aliases, true)
}

// IsIPv4Only reports whether existing is exactly what 0.7.x wrote for
// aliases: the managed block with 127.0.0.1 rows and no ::1 rows. A rewrite
// brings it up to date.
func IsIPv4Only(existing string, aliases []string) bool {
	old, err := rewrite(existing, aliases, false)
	return err == nil && unchanged(existing, old)
}

// rewrite is Rewrite with the ::1 rows optional.
func rewrite(existing string, aliases []string, ipv6 bool) (string, error) {
	sorted, err := normalize(aliases)
	if err != nil {
		return "", err
	}
	lines := splitLines(existing)
	start, end := findBlock(lines)
	var before, after []string
	// lastIn names the part that holds the file's last line, which has no
	// terminator when the file lacks a final newline.
	lastIn := "before"
	switch {
	case start >= 0 && end >= 0:
		before, after = lines[:start], lines[end+1:]
		lastIn = "after"
	case start >= 0:
		// Unterminated block: drop only the orphan marker and the block
		// entries right after it; everything else is foreign content.
		n := start + 1
		for n < len(lines) && isEntry(lines[n]) {
			n++
		}
		before = append(append([]string{}, lines[:start]...), lines[n:]...)
		if n == len(lines) {
			lastIn = ""
		}
	default:
		before = lines
	}
	// Lines outside the block keep their own bytes, a trailing \r included.
	// Only the block, and a newline added before it or after it, take the
	// file's dominant ending.
	missingFinal := len(lines) > 0 && !strings.HasSuffix(existing, "\n")
	voters := [2][]string{before, after}
	switch {
	case !missingFinal || lastIn == "" || (lastIn == "after" && len(after) == 0) || (lastIn == "before" && len(before) == 0):
	case lastIn == "after":
		voters[1] = after[:len(after)-1]
	default:
		voters[0] = before[:len(before)-1]
	}
	eol := lineEnding(voters[0], voters[1])
	type segment struct{ text, term string }
	var out []segment
	add := func(ls []string, holdsLast bool) {
		for i, l := range ls {
			term := "\n"
			if missingFinal && holdsLast && i == len(ls)-1 {
				term = ""
			}
			out = append(out, segment{l, term})
		}
	}
	add(before, lastIn == "before")
	for _, l := range blockFor(sorted, ipv6) {
		out = append(out, segment{l, eol})
	}
	add(after, lastIn == "after")
	var b strings.Builder
	for i, sg := range out {
		b.WriteString(sg.text)
		switch {
		case sg.term != "":
			b.WriteString(sg.term)
		case i < len(out)-1:
			b.WriteString(eol)
		}
	}
	return b.String(), nil
}

// withPage returns aliases with Nearhost's page, hostname.PageHost, added
// when there are any: every block Nearhost writes holds the page's name.
// Current macOS, Windows 11, Linux with systemd-resolved, Chrome and Firefox
// resolve it to loopback themselves; the line covers resolvers that do not.
// With no aliases there is no block, and no line for the page either.
func withPage(aliases []string) []string {
	if len(aliases) == 0 {
		return aliases
	}
	return append(slices.Clone(aliases), hostname.PageHost)
}

// UpToDate reports whether existing already holds the block for aliases, with
// or without the page's line. A block that lacks only that line counts as up
// to date, so Nearhost never asks for a password to add it alone: browsers
// resolve *.localhost themselves. It is the one comparison that Ensure and
// doctor share.
func UpToDate(existing string, aliases []string) bool {
	for _, want := range [][]string{aliases, withPage(aliases)} {
		if updated, err := Rewrite(existing, want); err == nil && unchanged(existing, updated) {
			return true
		}
	}
	return false
}

// Aliases returns the aliases currently in the managed block. The page's
// line, hostname.PageHost, is no alias and is left out.
func Aliases(existing string) []string {
	lines := splitLines(existing)
	start, end := findBlock(lines)
	if start < 0 || end < 0 {
		return nil
	}
	var out []string
	for _, l := range lines[start+1 : end] {
		if isEntry(l) && strings.Fields(l)[1] != hostname.PageHost {
			out = append(out, strings.Fields(l)[1])
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
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
// in its parents) is resolved first and the file it points to is replaced; a
// symlink whose target does not exist is refused and left as it is.
// On Linux the SELinux label and POSIX ACL are carried over; macOS ACLs and
// other extended attributes are not.
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
	if err := replaceFile(path, updated); err != nil {
		return err
	}
	// The file is right whether or not the flush works, and Windows' DNS
	// client also watches the file, so a failed flush is not an error.
	_ = flushDNS()
	return nil
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
	} else if li, lerr := os.Lstat(path); lerr == nil && li.Mode()&os.ModeSymlink != 0 {
		// The path is there, as a link to nothing. Renaming a new file over
		// it would put a regular file where the link was, and writing where
		// it points would create a file in a place nobody chose.
		dest, _ := os.Readlink(path)
		return fmt.Errorf("write %s: it is a symbolic link to %s, which does not exist; create that file or remove the link", path, dest)
	}

	// Before any temporary file exists, so a refused update leaves nothing.
	if isReadOnly(target) {
		return &readOnlyError{path: target}
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
	if info != nil && elevate.Elevated() {
		if err = chownLike(tmp, info); err != nil {
			return fmt.Errorf("write %s: %w", tmpName, err)
		}
	}
	// Mode before the ACL: setting the ACL then fixes the mask the copy carries.
	if err = tmp.Chmod(mode); err != nil {
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if info != nil {
		if err = copyLabels(target, tmpName); err != nil {
			return fmt.Errorf("write %s: keep label or ACL: %w", path, err)
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
// a change is needed (see UpToDate). apply is given aliases with the page's
// name added, so the block it writes holds it.
func Ensure(path string, aliases []string, apply Applier) (bool, error) {
	existing, err := read(path)
	if err != nil {
		return false, err
	}
	if _, err := Rewrite(existing, aliases); err != nil {
		return false, err
	}
	if UpToDate(existing, aliases) {
		return false, nil
	}
	if err := apply(withPage(aliases)); err != nil {
		return false, err
	}
	return true, nil
}

// ElevatedApplier runs `<executable> hosts apply --file <path>` with aliases
// on its input, through e. Where e takes a password (sudo), prompt is its
// prompt, used when interactive; otherwise it never prompts, for credentials
// it has cached, where a prompt could only land on a terminal in use, and
// fails if it needs a password. Where it does not (the UAC prompt on
// Windows), Windows asks for consent itself and prompt and interactive are
// unused. When ctx ends while the command runs, which includes waiting for
// a password, sudo is asked to terminate (and killed if it stays), and the
// UAC elevator stops waiting and withdraws the input, so a later approval
// changes nothing; either way the applier returns ctx's error. When ctx has
// already ended nothing is started.
func ElevatedApplier(ctx context.Context, e elevate.Elevator, executable, path, prompt string, interactive bool) Applier {
	return func(aliases []string) error {
		err := e.Run(ctx, elevate.Command{
			Path:        executable,
			Args:        []string{"hosts", "apply", "--file", path},
			Input:       strings.Join(aliases, "\n") + "\n",
			Prompt:      prompt,
			Interactive: interactive,
		})
		if err != nil {
			return fmt.Errorf(elevatedApplyPrefix+"%w", err)
		}
		return nil
	}
}
